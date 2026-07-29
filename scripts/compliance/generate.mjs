#!/usr/bin/env node

import { execFileSync } from 'node:child_process';
import { createHash } from 'node:crypto';
import fs from 'node:fs';
import https from 'node:https';
import path from 'node:path';
import process from 'node:process';
import { fileURLToPath } from 'node:url';

const SCRIPT_DIR = path.dirname(fileURLToPath(import.meta.url));
const RELAY_ROOT = path.resolve(SCRIPT_DIR, '..', '..');

const COMPONENTS = [
  { id: 'relay', directory: 'hornets-nostr-relay', name: 'HORNETS Nostr Relay', repository: 'https://github.com/HORNET-Storage/hornets-nostr-relay', env: 'SOURCE_RELAY_REVISION' },
  { id: 'airlock', directory: 'airlock', name: 'Airlock', repository: 'https://github.com/HORNET-Storage/airlock', env: 'SOURCE_AIRLOCK_REVISION' },
  { id: 'hyperswarm', directory: 'hornets-hyperswarm', name: 'HORNETS Hyperswarm', repository: 'https://github.com/HORNET-Storage/hornets-hyperswarm', env: 'SOURCE_HYPERSWARM_REVISION' },
  { id: 'nosis-cli', directory: 'nosis-cli', name: 'Nosis CLI', repository: 'https://github.com/HORNET-Storage/nosis-cli', env: 'SOURCE_NOSIS_CLI_REVISION' },
  { id: 'panel', directory: 'hornets-relay-panel', name: 'HORNETS Relay Panel', repository: 'https://github.com/HORNET-Storage/HORNETS-Relay-Panel', env: 'SOURCE_RELAY_PANEL_REVISION' },
];

const LEGAL_FILE_PATTERN = /^(unlicense|license|licence|copying|notice|patents)([._-].*)?$/i;
const ALLOWED_LICENSES = new Set([
  '0BSD',
  'Apache-2.0',
  'BlueOak-1.0.0',
  'BSD-2-Clause',
  'BSD-3-Clause',
  'ISC',
  'MIT',
  'MPL-2.0',
  'Unlicense',
  'Zlib',
]);
const FORBIDDEN_LICENSE_PATTERN = /(hippocratic|json|commons clause|business source|busl|sspl|unlicensed)/i;
const ASSET_EXTENSIONS = new Set(['.avif', '.eot', '.gif', '.ico', '.jpeg', '.jpg', '.png', '.svg', '.ttf', '.webp', '.woff', '.woff2']);
const CURATED_LICENSE_EVIDENCE = JSON.parse(fs.readFileSync(path.join(SCRIPT_DIR, 'license-evidence.json'), 'utf8'));

function parseArgs(argv) {
  const result = new Map();
  for (let index = 0; index < argv.length; index += 1) {
    const token = argv[index];
    if (!token.startsWith('--')) throw new Error(`Unexpected argument: ${token}`);
    const key = token.slice(2);
    const next = argv[index + 1];
    if (!next || next.startsWith('--')) {
      result.set(key, true);
    } else {
      result.set(key, next);
      index += 1;
    }
  }
  return result;
}

function requiredArg(args, name) {
  const value = args.get(name);
  if (!value || value === true) throw new Error(`Missing required --${name}`);
  return value;
}

function normalizeSlashes(value) {
  return value.replace(/\\/g, '/');
}

function safeName(value) {
  return value.replace(/^@/, '').replace(/[^A-Za-z0-9._-]+/g, '__');
}

function isWithin(child, parent) {
  const relative = path.relative(parent, child);
  return relative === '' || (!relative.startsWith('..') && !path.isAbsolute(relative));
}

function sha256File(filePath) {
  const hash = createHash('sha256');
  hash.update(fs.readFileSync(filePath));
  return hash.digest('hex');
}

function listFilesRecursive(root) {
  if (!fs.existsSync(root)) return [];
  const files = [];
  const queue = [root];
  while (queue.length > 0) {
    const current = queue.pop();
    for (const entry of fs.readdirSync(current, { withFileTypes: true })) {
      const absolute = path.join(current, entry.name);
      if (entry.isDirectory()) queue.push(absolute);
      else if (entry.isFile()) files.push(absolute);
    }
  }
  return files.sort();
}

function legalFiles(directory) {
  if (!fs.existsSync(directory)) return [];
  return fs.readdirSync(directory, { withFileTypes: true })
    .filter((entry) => entry.isFile() && LEGAL_FILE_PATTERN.test(entry.name))
    .map((entry) => path.join(directory, entry.name))
    .sort();
}

function normalizeDeclaredLicense(value) {
  if (Array.isArray(value)) return value.map(normalizeDeclaredLicense).filter(Boolean).join(' OR ');
  if (value && typeof value === 'object') return normalizeDeclaredLicense(value.type || value.name || '');
  if (typeof value !== 'string') return '';
  return value.trim()
    .replace(/^Apache(?: License)?(?:,? Version)? 2(?:\.0)?$/i, 'Apache-2.0')
    .replace(/^BSD(?: License)?$/i, 'BSD-2-Clause')
    .replace(/^The Unlicense$/i, 'Unlicense');
}

function classifyLicenseText(text) {
  const normalized = text.replace(/\s+/g, ' ').replace(/&lt;/gi, '<').replace(/&gt;/gi, '>');
  if (/Mozilla Public License.*Version 2\.0/i.test(normalized)) return 'MPL-2.0';
  if (/Apache License.*Version 2\.0/i.test(normalized)) return 'Apache-2.0';
  if (/Permission to use, copy, modify, and(?:\/or)? distribute this software for any purpose with or without fee/i.test(normalized)) return 'ISC';
  if (/Permission is hereby granted, free of charge, to any person obtaining a copy/i.test(normalized)) return 'MIT';
  if (/Redistribution and use(?: of this software)? in source and binary forms/i.test(normalized) && /Neither the name/i.test(normalized)) return 'BSD-3-Clause';
  if (/Redistribution and use(?: of this software)? in source and binary forms/i.test(normalized)) return 'BSD-2-Clause';
  if (/free and unencumbered software released into the public domain/i.test(normalized)) return 'Unlicense';
  if (/Blue Oak Model License.*Version 1\.0\.0/i.test(normalized)) return 'BlueOak-1.0.0';
  if (/This software is provided ['"]as-is['"]/i.test(normalized) && /altered source versions must be plainly marked/i.test(normalized)) return 'Zlib';
  return '';
}

function normalizeLicenseId(value) {
  return value.trim().replace(/^\(+|\)+$/g, '').replace(/\*$/, '');
}

function licenseExpressionAllowed(expression) {
  if (!expression || FORBIDDEN_LICENSE_PATTERN.test(expression)) return false;
  if (/SEE LICENSE IN/i.test(expression)) return false;
  const cleaned = expression.replace(/[()]/g, ' ').replace(/\s+/g, ' ').trim();
  return cleaned.split(/\s+OR\s+/i).some((alternative) => {
    return alternative.split(/\s+AND\s+/i).every((part) => ALLOWED_LICENSES.has(normalizeLicenseId(part)));
  });
}

function concludeLicense(declared, files) {
  const text = files
    .filter((file) => !/^(notice|patents)/i.test(path.basename(file)))
    .map((file) => fs.readFileSync(file, 'utf8'))
    .join('\n');
  const detected = classifyLicenseText(text);
  if (detected && ALLOWED_LICENSES.has(detected)) return { concluded: detected, accepted: true };
  return { concluded: detected || declared || 'UNKNOWN', accepted: false };
}

function normalizeRepository(repository) {
  let value = typeof repository === 'string' ? repository : repository?.url;
  if (!value) return '';
  value = value
    .replace(/^git\+/, '')
    .replace(/^git:\/\/github\.com\//, 'https://github.com/')
    .replace(/^ssh:\/\/git@github\.com\//, 'https://github.com/')
    .replace(/^git@github\.com:/, 'https://github.com/')
    .replace(/^github:/, 'https://github.com/')
    .replace(/\.git(?=\/|$)/, '');
  return value;
}

function githubCoordinates(repository) {
  const normalized = normalizeRepository(repository);
  const match = normalized.match(/github\.com[/:]([^/]+)\/([^/#]+)/i);
  if (!match) return null;
  return { owner: match[1], repository: match[2].replace(/\.git$/, '') };
}

function npmPackageUrl(name, version) {
  return `https://www.npmjs.com/package/${encodeURIComponent(name)}/v/${encodeURIComponent(version)}`;
}

function npmTarballUrl(name, version) {
  const encodedName = name.startsWith('@') ? `@${name.slice(1).replace('/', '%2f')}` : name;
  const archiveName = name.includes('/') ? name.split('/')[1] : name;
  return `https://registry.npmjs.org/${encodedName}/-/${archiveName}-${version}.tgz`;
}

function expressionIncludesLicense(expression, license) {
  const cleaned = expression.replace(/[()]/g, ' ').replace(/\s+/g, ' ').trim();
  return cleaned.split(/\s+(?:OR|AND)\s+/i).map(normalizeLicenseId).includes(license);
}

function canonicalLicenseText(license, notice) {
  const terms = {
    ISC: [
      'Permission to use, copy, modify, and/or distribute this software for any purpose with or without fee is hereby granted, provided that the above copyright notice and this permission notice appear in all copies.',
      '',
      'THE SOFTWARE IS PROVIDED "AS IS" AND THE AUTHOR DISCLAIMS ALL WARRANTIES WITH REGARD TO THIS SOFTWARE INCLUDING ALL IMPLIED WARRANTIES OF MERCHANTABILITY AND FITNESS. IN NO EVENT SHALL THE AUTHOR BE LIABLE FOR ANY SPECIAL, DIRECT, INDIRECT, OR CONSEQUENTIAL DAMAGES OR ANY DAMAGES WHATSOEVER RESULTING FROM LOSS OF USE, DATA OR PROFITS, WHETHER IN AN ACTION OF CONTRACT, NEGLIGENCE OR OTHER TORTIOUS ACTION, ARISING OUT OF OR IN CONNECTION WITH THE USE OR PERFORMANCE OF THIS SOFTWARE.',
    ].join('\n'),
    MIT: [
      'Permission is hereby granted, free of charge, to any person obtaining a copy of this software and associated documentation files (the "Software"), to deal in the Software without restriction, including without limitation the rights to use, copy, modify, merge, publish, distribute, sublicense, and/or sell copies of the Software, and to permit persons to whom the Software is furnished to do so, subject to the following conditions:',
      '',
      'The above copyright notice and this permission notice shall be included in all copies or substantial portions of the Software.',
      '',
      'THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY, FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM, OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN THE SOFTWARE.',
    ].join('\n'),
  };
  if (!terms[license]) throw new Error(`no canonical text is configured for ${license}`);
  if (!/^Copyright\b/i.test(notice)) throw new Error('curated evidence has no copyright notice');
  const text = `${notice}\n\n${terms[license]}\n`;
  if (classifyLicenseText(text) !== license) throw new Error(`generated ${license} text did not classify correctly`);
  return text;
}

function writeCuratedLicenseEvidence(coordinate, observedExpression, observedRevision, destination, outputRoot, details) {
  const entry = CURATED_LICENSE_EVIDENCE[coordinate];
  if (!entry) throw new Error(`no exact-version curated evidence exists for ${coordinate}`);
  const upstreamExpression = normalizeDeclaredLicense(entry.upstreamLicenseExpression);
  if (!licenseExpressionAllowed(upstreamExpression) || !ALLOWED_LICENSES.has(entry.license) || !expressionIncludesLicense(upstreamExpression, entry.license)) {
    throw new Error(`invalid curated license choice for ${coordinate}`);
  }
  if (observedExpression && normalizeDeclaredLicense(observedExpression) !== upstreamExpression) {
    throw new Error(`curated and observed license declarations differ for ${coordinate}`);
  }
  if (!/^[0-9a-f]{40}$/i.test(observedRevision) || observedRevision.toLowerCase() !== String(entry.upstreamRevision).toLowerCase()) {
    throw new Error(`curated and observed revisions differ for ${coordinate}`);
  }
  if (coordinate.startsWith('npm:') && (!details.integrity || !details.shasum || !/^https:\/\//.test(details.exactArchive || ''))) {
    throw new Error(`curated npm archive evidence is incomplete for ${coordinate}`);
  }
  if (!/^https:\/\//.test(entry.evidence) || !entry.rationale) throw new Error(`curated evidence is incomplete for ${coordinate}`);

  fs.mkdirSync(destination, { recursive: true });
  const licensePath = path.join(destination, 'CURATED-LICENSE.txt');
  fs.writeFileSync(licensePath, canonicalLicenseText(entry.license, entry.notice));
  const provenancePath = path.join(destination, 'PROVENANCE.json');
  fs.writeFileSync(provenancePath, `${JSON.stringify({
    evidenceKind: 'curated-exact-version-license-completion',
    coordinate,
    licenseChoice: entry.license,
    upstreamLicenseExpression: upstreamExpression,
    upstreamRevision: entry.upstreamRevision,
    upstreamEvidence: entry.evidence,
    rationale: entry.rationale,
    ...details,
  }, null, 2)}\n`);
  return {
    concluded: entry.license,
    licenseFiles: [normalizeSlashes(path.relative(outputRoot, licensePath))],
    evidence: normalizeSlashes(path.relative(outputRoot, provenancePath)),
  };
}

function copyLegalFiles(files, destination, outputRoot) {
  fs.mkdirSync(destination, { recursive: true });
  return files.map((source) => {
    const target = path.join(destination, path.basename(source));
    fs.copyFileSync(source, target);
    return normalizeSlashes(path.relative(outputRoot, target));
  });
}

function indexInstalledPackages(projectRoot) {
  const index = new Map();
  const visitedNodeModules = new Set();

  function addPackage(packageDirectory) {
    let realDirectory;
    try {
      realDirectory = fs.realpathSync(packageDirectory);
    } catch {
      return;
    }
    const manifestPath = path.join(realDirectory, 'package.json');
    if (!fs.existsSync(manifestPath)) return;
    const manifest = JSON.parse(fs.readFileSync(manifestPath, 'utf8'));
    if (!manifest.name || !manifest.version) return;
    if (!index.has(manifest.name)) index.set(manifest.name, new Map());
    index.get(manifest.name).set(`${manifest.version}:${realDirectory}`, { directory: realDirectory, manifest });
    walkNodeModules(path.join(realDirectory, 'node_modules'));
  }

  function walkNodeModules(nodeModulesDirectory) {
    if (!fs.existsSync(nodeModulesDirectory)) return;
    let realDirectory;
    try {
      realDirectory = fs.realpathSync(nodeModulesDirectory);
    } catch {
      return;
    }
    if (visitedNodeModules.has(realDirectory)) return;
    visitedNodeModules.add(realDirectory);

    for (const entry of fs.readdirSync(realDirectory, { withFileTypes: true })) {
      if (!entry.isDirectory() || entry.name === '.bin') continue;
      const entryPath = path.join(realDirectory, entry.name);
      if (entry.name.startsWith('@')) {
        for (const scoped of fs.readdirSync(entryPath, { withFileTypes: true })) {
          if (scoped.isDirectory()) addPackage(path.join(entryPath, scoped.name));
        }
      } else {
        addPackage(entryPath);
      }
    }
  }

  walkNodeModules(path.join(projectRoot, 'node_modules'));
  return index;
}

function packageNameFromModulePath(input) {
  const normalized = normalizeSlashes(input).split('?')[0];
  const marker = 'node_modules/';
  const index = normalized.lastIndexOf(marker);
  if (index < 0) return '';
  const parts = normalized.slice(index + marker.length).split('/').filter(Boolean);
  if (parts.length === 0) return '';
  return parts[0].startsWith('@') && parts[1] ? `${parts[0]}/${parts[1]}` : parts[0];
}

function packageNamesFromSidecar(sidecarRoot) {
  const metafilePath = path.join(sidecarRoot, 'dist', 'bundle-meta.json');
  if (!fs.existsSync(metafilePath)) throw new Error(`Missing sidecar dependency metadata: ${metafilePath}`);
  const metafile = JSON.parse(fs.readFileSync(metafilePath, 'utf8'));
  const names = new Set();
  for (const input of Object.keys(metafile.inputs || {})) {
    const name = packageNameFromModulePath(input);
    if (name) names.add(name);
  }
  return names;
}

function packageNamesFromPanel(panelRoot) {
  const buildRoot = path.join(panelRoot, 'build');
  const mapFiles = listFilesRecursive(buildRoot).filter((file) => file.endsWith('.map'));
  if (mapFiles.length === 0) throw new Error(`No panel production source maps found under ${buildRoot}; cannot prove the bundled dependency set`);
  const names = new Set();
  for (const mapFile of mapFiles) {
    let sourceMap;
    try {
      sourceMap = JSON.parse(fs.readFileSync(mapFile, 'utf8'));
    } catch (error) {
      throw new Error(`Invalid source map ${mapFile}: ${error.message}`);
    }
    for (const source of sourceMap.sources || []) {
      const name = packageNameFromModulePath(source);
      if (name) names.add(name);
    }
  }
  return names;
}

function embeddedLicenseFiles(directory) {
  for (const entry of fs.readdirSync(directory, { withFileTypes: true })) {
    if (!entry.isFile() || !/^readme(?:[._-].*)?$/i.test(entry.name)) continue;
    const readme = path.join(directory, entry.name);
    const detected = classifyLicenseText(fs.readFileSync(readme, 'utf8'));
    if (detected && ALLOWED_LICENSES.has(detected)) return [readme];
  }
  return [];
}

async function fetchNpmLicenseEvidence(manifest, destination, outputRoot) {
  const encodedName = manifest.name.startsWith('@') ? `@${manifest.name.slice(1).replace('/', '%2f')}` : encodeURIComponent(manifest.name);
  const metadataUrl = `https://registry.npmjs.org/${encodedName}/${encodeURIComponent(manifest.version)}`;
  const metadata = JSON.parse(await requestText(metadataUrl));
  if (metadata.name !== manifest.name || metadata.version !== manifest.version) throw new Error('npm registry returned different package coordinates');
  const installedDeclared = normalizeDeclaredLicense(manifest.license ?? manifest.licenses);
  const registryDeclared = normalizeDeclaredLicense(metadata.license ?? metadata.licenses);
  if (!licenseExpressionAllowed(installedDeclared) || !licenseExpressionAllowed(registryDeclared)) throw new Error(`unapproved npm license declaration (${installedDeclared || 'UNKNOWN'} / ${registryDeclared || 'UNKNOWN'})`);
  if (installedDeclared !== registryDeclared) throw new Error(`installed and registry license declarations differ (${installedDeclared} / ${registryDeclared})`);

  const coordinates = githubCoordinates(metadata.repository || manifest.repository);
  if (metadata.gitHead && /^[0-9a-f]{40}$/i.test(metadata.gitHead) && coordinates) {
    const candidates = ['LICENSE', 'LICENSE.md', 'LICENSE.txt', 'LICENCE', 'LICENCE.md', 'COPYING', 'COPYING.md', 'LICENSE-MIT', 'LICENSE-APACHE', 'README.md'];
    for (const candidate of candidates) {
      const url = `https://raw.githubusercontent.com/${coordinates.owner}/${coordinates.repository}/${metadata.gitHead}/${candidate}`;
      let text;
      try {
        text = await requestText(url);
      } catch {
        continue;
      }
      const detected = classifyLicenseText(text);
      if (!detected || !ALLOWED_LICENSES.has(detected)) continue;
      fs.mkdirSync(destination, { recursive: true });
      const licensePath = path.join(destination, candidate === 'README.md' ? 'UPSTREAM-README-LICENSE.md' : 'UPSTREAM-LICENSE');
      fs.writeFileSync(licensePath, text);
      const provenancePath = path.join(destination, 'PROVENANCE.json');
      fs.writeFileSync(provenancePath, `${JSON.stringify({
        package: `${manifest.name}@${manifest.version}`,
        registryMetadata: metadataUrl,
        exactArchive: metadata.dist?.tarball || npmTarballUrl(manifest.name, manifest.version),
        integrity: metadata.dist?.integrity || '',
        shasum: metadata.dist?.shasum || '',
        repository: normalizeRepository(metadata.repository || manifest.repository),
        gitCommit: metadata.gitHead,
        upstreamLicense: url,
        detectedLicense: detected,
      }, null, 2)}\n`);
      return {
        concluded: detected,
        licenseFiles: [normalizeSlashes(path.relative(outputRoot, licensePath))],
        evidence: normalizeSlashes(path.relative(outputRoot, provenancePath)),
        exactArchive: metadata.dist?.tarball || npmTarballUrl(manifest.name, manifest.version),
      };
    }
  }

  const curated = writeCuratedLicenseEvidence(
    `npm:${manifest.name}@${manifest.version}`,
    registryDeclared,
    metadata.gitHead || '',
    destination,
    outputRoot,
    {
      package: `${manifest.name}@${manifest.version}`,
      registryMetadata: metadataUrl,
      exactArchive: metadata.dist?.tarball || npmTarballUrl(manifest.name, manifest.version),
      integrity: metadata.dist?.integrity || '',
      shasum: metadata.dist?.shasum || '',
      repository: normalizeRepository(metadata.repository || manifest.repository),
      gitCommit: metadata.gitHead || '',
    },
  );
  return { ...curated, exactArchive: metadata.dist?.tarball || npmTarballUrl(manifest.name, manifest.version) };
}

async function collectNodeRecords(projectRoot, scope, packageNames, outputRoot, failures) {
  const packageIndex = indexInstalledPackages(projectRoot);
  const records = [];
  for (const packageName of [...packageNames].sort()) {
    const matches = packageIndex.get(packageName);
    if (!matches || matches.size === 0) {
      failures.push(`${scope}: bundled package ${packageName} is not present in node_modules`);
      continue;
    }
    for (const { directory, manifest } of matches.values()) {
      let files = legalFiles(directory);
      if (files.length === 0) files = embeddedLicenseFiles(directory);
      const declared = normalizeDeclaredLicense(manifest.license ?? manifest.licenses);
      let conclusion = concludeLicense(declared, files);
      const key = `${safeName(manifest.name)}@${safeName(manifest.version)}`;
      const destination = path.join(outputRoot, 'THIRD_PARTY_LICENSES', scope, key);
      let copied = copyLegalFiles(files, destination, outputRoot);
      let licenseEvidence = '';
      let exactArchive = npmTarballUrl(manifest.name, manifest.version);
      if (!conclusion.accepted || copied.length === 0) {
        try {
          const fetched = await fetchNpmLicenseEvidence(manifest, destination, outputRoot);
          conclusion = { concluded: fetched.concluded, accepted: true };
          copied = [...copied, ...fetched.licenseFiles];
          licenseEvidence = fetched.evidence;
          exactArchive = fetched.exactArchive;
        } catch (error) {
          failures.push(`${scope}: ${manifest.name}@${manifest.version} license evidence lookup failed (${error.message})`);
        }
      }
      const accepted = conclusion.accepted && copied.length > 0;
      if (!accepted) failures.push(`${scope}: ${manifest.name}@${manifest.version} has unacceptable or incomplete license data (${conclusion.concluded})`);
      records.push({
        ecosystem: 'npm',
        scope,
        name: manifest.name,
        version: manifest.version,
        licenseDeclared: declared || 'UNKNOWN',
        licenseConcluded: conclusion.concluded,
        status: accepted ? 'accepted' : 'rejected',
        source: normalizeRepository(manifest.repository),
        package: npmPackageUrl(manifest.name, manifest.version),
        exactArchive,
        licenseFiles: copied,
        ...(licenseEvidence ? { licenseEvidence } : {}),
      });
    }
  }
  return records;
}

function goSource(modulePath, version) {
  const exact = `https://pkg.go.dev/${modulePath}@${version}`;
  if (!modulePath.startsWith('github.com/')) return { source: '', package: exact };
  const parts = modulePath.split('/');
  const repository = `https://github.com/${parts[1]}/${parts[2]}`;
  return { source: `${repository}/tree/${version}`, package: exact };
}

function goModuleOrigin(modulePath, version) {
  const metadata = JSON.parse(execFileSync('go', ['mod', 'download', '-json', `${modulePath}@${version}`], { cwd: RELAY_ROOT, encoding: 'utf8' }));
  if (metadata.Path !== modulePath || metadata.Version !== version || !metadata.Sum || !metadata.GoModSum) {
    throw new Error(`Go module download metadata is incomplete for ${modulePath}@${version}`);
  }
  if (metadata.Origin?.VCS !== 'git' || !/^[0-9a-f]{40}$/i.test(metadata.Origin?.Hash || '')) {
    throw new Error(`immutable Go module origin is unavailable for ${modulePath}@${version}`);
  }
  return metadata;
}

async function fetchGoLicenseEvidence(modulePath, version, destination, outputRoot) {
  if (!modulePath.startsWith('github.com/')) throw new Error('module is not hosted on GitHub');
  const parts = modulePath.split('/');
  const repository = `https://github.com/${parts[1]}/${parts[2]}`;
  const refs = new Set([version.replace(/\+incompatible$/, '')]);
  const pseudoVersion = version.match(/-([0-9a-f]{12,40})$/i);
  if (pseudoVersion) refs.add(pseudoVersion[1]);
  const candidates = ['LICENSE', 'LICENSE.md', 'LICENSE.txt', 'LICENCE', 'LICENCE.md', 'COPYING', 'COPYING.md', 'UNLICENSE', 'LICENSE-MIT', 'LICENSE-APACHE'];
  for (const ref of refs) {
    for (const candidate of candidates) {
      const url = `https://raw.githubusercontent.com/${parts[1]}/${parts[2]}/${ref}/${candidate}`;
      let text;
      try {
        text = await requestText(url);
      } catch {
        continue;
      }
      const detected = classifyLicenseText(text);
      if (!detected || !ALLOWED_LICENSES.has(detected)) continue;
      fs.mkdirSync(destination, { recursive: true });
      const licensePath = path.join(destination, 'UPSTREAM-LICENSE');
      fs.writeFileSync(licensePath, text);
      const provenancePath = path.join(destination, 'PROVENANCE.json');
      fs.writeFileSync(provenancePath, `${JSON.stringify({
        module: `${modulePath}@${version}`,
        package: `https://pkg.go.dev/${modulePath}@${version}`,
        repository,
        sourceRef: ref,
        upstreamLicense: url,
        detectedLicense: detected,
      }, null, 2)}\n`);
      return {
        concluded: detected,
        licenseFiles: [normalizeSlashes(path.relative(outputRoot, licensePath))],
        evidence: normalizeSlashes(path.relative(outputRoot, provenancePath)),
      };
    }
  }

  const moduleEvidence = goModuleOrigin(modulePath, version);
  return writeCuratedLicenseEvidence(
    `go:${modulePath}@${version}`,
    '',
    moduleEvidence.Origin.Hash,
    destination,
    outputRoot,
    {
      module: `${modulePath}@${version}`,
      package: `https://pkg.go.dev/${modulePath}@${version}`,
      repository,
      moduleVersion: version,
      moduleChecksum: moduleEvidence.Sum,
      goModChecksum: moduleEvidence.GoModSum,
      sourceRef: moduleEvidence.Origin.Hash,
    },
  );
}

async function collectGoRecords(workspace, projectRoot, target, scope, outputRoot, failures) {
  const template = '{{with .Module}}{{if not .Main}}{{.Path}}\t{{.Version}}\t{{.Dir}}\t{{.Sum}}{{end}}{{end}}';
  const output = execFileSync('go', ['list', '-deps', '-f', template, target], { cwd: projectRoot, encoding: 'utf8' });
  const modules = new Map();
  for (const line of output.split(/\r?\n/)) {
    if (!line.trim()) continue;
    const [modulePath, version, directory, sum] = line.split('\t');
    if (!modulePath || !directory || isWithin(path.resolve(directory), workspace)) continue;
    modules.set(`${modulePath}@${version}`, { modulePath, version, directory, sum });
  }

  const records = [];
  for (const { modulePath, version, directory, sum } of [...modules.values()].sort((a, b) => a.modulePath.localeCompare(b.modulePath))) {
    const files = legalFiles(directory);
    let conclusion = concludeLicense('', files);
    const key = `${safeName(modulePath)}@${safeName(version)}`;
    const destination = path.join(outputRoot, 'THIRD_PARTY_LICENSES', scope, key);
    let copied = copyLegalFiles(files, destination, outputRoot);
    let licenseEvidence = '';
    if (!conclusion.accepted || copied.length === 0) {
      try {
        const fetched = await fetchGoLicenseEvidence(modulePath, version, destination, outputRoot);
        conclusion = { concluded: fetched.concluded, accepted: true };
        copied = [...copied, ...fetched.licenseFiles];
        licenseEvidence = fetched.evidence;
      } catch (error) {
        failures.push(`${scope}: ${modulePath}@${version} license evidence lookup failed (${error.message})`);
      }
    }
    const accepted = conclusion.accepted && copied.length > 0;
    if (!accepted) failures.push(`${scope}: ${modulePath}@${version} has unacceptable or incomplete license data (${conclusion.concluded})`);
    records.push({
      ecosystem: 'go',
      scope,
      name: modulePath,
      version,
      checksum: sum,
      licenseDeclared: conclusion.concluded,
      licenseConcluded: conclusion.concluded,
      status: accepted ? 'accepted' : 'rejected',
      ...goSource(modulePath, version),
      licenseFiles: copied,
      ...(licenseEvidence ? { licenseEvidence } : {}),
    });
  }
  return records;
}

function requestText(url, redirects = 0) {
  if (redirects > 5) return Promise.reject(new Error(`Too many redirects fetching ${url}`));
  return new Promise((resolve, reject) => {
    https.get(url, { headers: { 'User-Agent': 'hornets-foss-compliance-generator' } }, (response) => {
      if (response.statusCode >= 300 && response.statusCode < 400 && response.headers.location) {
        response.resume();
        resolve(requestText(new URL(response.headers.location, url).toString(), redirects + 1));
        return;
      }
      if (response.statusCode !== 200) {
        response.resume();
        reject(new Error(`HTTP ${response.statusCode} fetching ${url}`));
        return;
      }
      response.setEncoding('utf8');
      let body = '';
      response.on('data', (chunk) => { body += chunk; });
      response.on('end', () => resolve(body));
    }).on('error', reject);
  });
}

async function nodeRuntimeRecord(outputRoot, failures) {
  const version = process.versions.node;
  const candidates = [];
  let current = path.dirname(process.execPath);
  for (let depth = 0; depth < 5; depth += 1) {
    candidates.push(path.join(current, 'LICENSE'));
    candidates.push(path.join(current, 'LICENSE.txt'));
    current = path.dirname(current);
  }
  let licenseText = '';
  for (const candidate of candidates) {
    if (fs.existsSync(candidate)) {
      const text = fs.readFileSync(candidate, 'utf8');
      if (/Node\.js is licensed|Permission is hereby granted/i.test(text)) {
        licenseText = text;
        break;
      }
    }
  }
  const source = `https://github.com/nodejs/node/tree/v${version}`;
  if (!licenseText) licenseText = await requestText(`https://raw.githubusercontent.com/nodejs/node/v${version}/LICENSE`);
  const destination = path.join(outputRoot, 'THIRD_PARTY_LICENSES', 'node-runtime', `node@${safeName(version)}`);
  fs.mkdirSync(destination, { recursive: true });
  const licensePath = path.join(destination, 'LICENSE');
  fs.writeFileSync(licensePath, licenseText);
  if (!/Node\.js is licensed|Permission is hereby granted/i.test(licenseText)) failures.push(`node-runtime: Node.js v${version} license text could not be validated`);
  return {
    ecosystem: 'runtime',
    scope: 'node-runtime',
    name: 'Node.js',
    version,
    licenseDeclared: 'MIT and bundled third-party terms',
    licenseConcluded: 'MIT and bundled third-party terms',
    status: 'accepted',
    source,
    package: `https://nodejs.org/download/release/v${version}/`,
    licenseFiles: [normalizeSlashes(path.relative(outputRoot, licensePath))],
  };
}

function copyFirstParty(workspace, outputRoot, failures) {
  const records = [];
  for (const component of COMPONENTS) {
    const componentRoot = path.join(workspace, component.directory);
    const files = legalFiles(componentRoot);
    const license = files.find((file) => /^licen[cs]e/i.test(path.basename(file)));
    if (!license) {
      failures.push(`first-party: ${component.name} has no top-level LICENSE file`);
      continue;
    }
    const destination = path.join(outputRoot, 'FIRST_PARTY', component.id);
    const copied = copyLegalFiles(files, destination, outputRoot);
    records.push({ id: component.id, name: component.name, license: 'MIT', licenseFiles: copied });
  }
  const panelRoot = path.join(workspace, 'hornets-relay-panel');
  for (const extra of ['ASSET-PROVENANCE.md']) {
    const source = path.join(panelRoot, extra);
    if (!fs.existsSync(source)) {
      failures.push(`panel: missing ${extra}`);
      continue;
    }
    fs.copyFileSync(source, path.join(outputRoot, extra));
  }
  return records;
}

function sourceManifest(args, workspace, artifact, failures) {
  const strict = args.has('require-immutable-sources');
  const components = COMPONENTS.map((component) => {
    const revisionArg = `${component.id}-revision`;
    const raw = args.get(revisionArg);
    const revision = typeof raw === 'string' ? raw : process.env[component.env] || 'working-tree';
    const immutable = /^[0-9a-f]{40}$/i.test(revision);
    if (strict && !immutable) failures.push(`source: ${component.name} does not have an immutable 40-character revision (${revision})`);
    return {
      id: component.id,
      name: component.name,
      repository: component.repository,
      revision,
      immutable,
      matchingSource: immutable ? `${component.repository}/tree/${revision}` : component.repository,
      localDirectory: normalizeSlashes(path.relative(workspace, path.join(workspace, component.directory))),
    };
  });
  return { schemaVersion: 1, artifact, components };
}

function assetInventory(panelRoot) {
  const buildRoot = path.join(panelRoot, 'build');
  return listFilesRecursive(buildRoot)
    .filter((file) => ASSET_EXTENSIONS.has(path.extname(file).toLowerCase()))
    .map((file) => ({
      path: normalizeSlashes(path.relative(buildRoot, file)),
      bytes: fs.statSync(file).size,
      sha256: sha256File(file),
    }));
}

function noticesMarkdown(records) {
  const lines = [
    '# Third-party notices',
    '',
    'This inventory was generated from the modules actually compiled into the Go binaries, the esbuild input graph embedded in the hyperswarm SEA, the panel production source maps, and the exact Node.js runtime used to create the SEA.',
    '',
    'The corresponding license and NOTICE files are stored under `THIRD_PARTY_LICENSES/`. Source and exact-package links below are provided to satisfy matching-source obligations and make review reproducible.',
    '',
    '| Scope | Component | Version | License | Exact source/package |',
    '| --- | --- | --- | --- | --- |',
  ];
  for (const record of records) {
    const link = record.package || record.source || '';
    lines.push(`| ${record.scope} | ${record.name} | ${record.version} | ${record.licenseConcluded} | ${link} |`);
  }
  lines.push('', '## MPL-2.0 corresponding source', '');
  const mpl = records.filter((record) => /MPL-2\.0/.test(record.licenseConcluded));
  if (mpl.length === 0) lines.push('No MPL-2.0 component was detected.');
  for (const record of mpl) lines.push(`- ${record.name}@${record.version}: ${record.package || record.source}`);
  lines.push('');
  return `${lines.join('\n')}\n`;
}

function sourceMarkdown(manifest) {
  const lines = [
    '# Matching source code',
    '',
    `Artifact: ${manifest.artifact}`,
    '',
    'The combined artifact is assembled from these exact first-party revisions:',
    '',
  ];
  for (const component of manifest.components) lines.push(`- ${component.name}: ${component.matchingSource} (${component.revision})`);
  lines.push('', 'Third-party exact source/package locations are recorded in `THIRD-PARTY-NOTICES.json` and summarized in `THIRD-PARTY-NOTICES.md`.', '');
  return lines.join('\n');
}

function writeChecksums(outputRoot) {
  const checksumPath = path.join(outputRoot, 'CHECKSUMS.sha256');
  const lines = listFilesRecursive(outputRoot)
    .filter((file) => file !== checksumPath)
    .map((file) => `${sha256File(file)}  ${normalizeSlashes(path.relative(outputRoot, file))}`);
  fs.writeFileSync(checksumPath, `${lines.join('\n')}\n`);
}

async function main() {
  const args = parseArgs(process.argv.slice(2));
  const workspace = path.resolve(requiredArg(args, 'workspace'));
  const outputRoot = path.resolve(requiredArg(args, 'output'));
  const artifact = String(args.get('artifact') || 'local-working-tree');
  if (!fs.existsSync(workspace)) throw new Error(`Workspace does not exist: ${workspace}`);
  if (outputRoot === workspace || outputRoot === path.parse(outputRoot).root) throw new Error(`Unsafe compliance output path: ${outputRoot}`);
  fs.rmSync(outputRoot, { recursive: true, force: true });
  fs.mkdirSync(outputRoot, { recursive: true });

  const failures = [];
  const firstParty = copyFirstParty(workspace, outputRoot, failures);
  const sources = sourceManifest(args, workspace, artifact, failures);

  const relayRoot = path.join(workspace, 'hornets-nostr-relay');
  const airlockRoot = path.join(workspace, 'airlock');
  const sidecarRoot = path.join(workspace, 'hornets-hyperswarm');
  const panelRoot = path.join(workspace, 'hornets-relay-panel');

  const [relayGoRecords, airlockGoRecords, sidecarRecords, panelRecords] = await Promise.all([
    collectGoRecords(workspace, relayRoot, './services/server/port', 'relay-binary', outputRoot, failures),
    collectGoRecords(workspace, airlockRoot, '.', 'airlock-binary', outputRoot, failures),
    collectNodeRecords(sidecarRoot, 'hyperswarm-sea', packageNamesFromSidecar(sidecarRoot), outputRoot, failures),
    collectNodeRecords(panelRoot, 'panel-web', packageNamesFromPanel(panelRoot), outputRoot, failures),
  ]);
  const thirdParty = [
    ...relayGoRecords,
    ...airlockGoRecords,
    ...sidecarRecords,
    ...panelRecords,
    await nodeRuntimeRecord(outputRoot, failures),
  ].sort((a, b) => `${a.scope}:${a.name}:${a.version}`.localeCompare(`${b.scope}:${b.name}:${b.version}`));

  const inventory = {
    schemaVersion: 1,
    artifact,
    policy: { allowedLicenses: [...ALLOWED_LICENSES].sort(), forbiddenPattern: FORBIDDEN_LICENSE_PATTERN.source },
    firstParty,
    thirdParty,
  };

  fs.writeFileSync(path.join(outputRoot, 'SOURCE-MANIFEST.json'), `${JSON.stringify(sources, null, 2)}\n`);
  fs.writeFileSync(path.join(outputRoot, 'SOURCE-CODE.md'), `${sourceMarkdown(sources)}\n`);
  fs.writeFileSync(path.join(outputRoot, 'THIRD-PARTY-NOTICES.json'), `${JSON.stringify(inventory, null, 2)}\n`);
  fs.writeFileSync(path.join(outputRoot, 'THIRD-PARTY-NOTICES.md'), noticesMarkdown(thirdParty));
  fs.writeFileSync(path.join(outputRoot, 'ASSET-INVENTORY.json'), `${JSON.stringify({ schemaVersion: 1, root: 'relay/web', assets: assetInventory(panelRoot) }, null, 2)}\n`);
  fs.writeFileSync(path.join(outputRoot, 'README.md'), '# License and source compliance pack\n\nSee `SOURCE-CODE.md`, `THIRD-PARTY-NOTICES.md`, `ASSET-PROVENANCE.md`, and their machine-readable JSON inventories. License and NOTICE texts are under `FIRST_PARTY/` and `THIRD_PARTY_LICENSES/`. `CHECKSUMS.sha256` protects this pack from accidental omission or modification.\n');
  writeChecksums(outputRoot);

  if (failures.length > 0) {
    throw new Error(`Compliance generation failed:\n- ${[...new Set(failures)].join('\n- ')}`);
  }
  console.log(`Generated compliance pack with ${firstParty.length} first-party projects and ${thirdParty.length} third-party records at ${outputRoot}`);
}

main().catch((error) => {
  console.error(error.stack || error.message);
  process.exit(1);
});
