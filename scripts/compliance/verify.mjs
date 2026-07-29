#!/usr/bin/env node

import { createHash } from 'node:crypto';
import fs from 'node:fs';
import path from 'node:path';
import process from 'node:process';

const REQUIRED_FILES = [
  'README.md',
  'SOURCE-CODE.md',
  'SOURCE-MANIFEST.json',
  'THIRD-PARTY-NOTICES.md',
  'THIRD-PARTY-NOTICES.json',
  'ASSET-PROVENANCE.md',
  'ASSET-INVENTORY.json',
  'CHECKSUMS.sha256',
];
const FIRST_PARTY = ['relay', 'airlock', 'hyperswarm', 'nosis-cli', 'panel'];
const REQUIRED_MPL = new Map([
  ['github.com/hashicorp/hcl', 'v1.0.0'],
  ['github.com/hashicorp/golang-lru/v2', 'v2.0.7'],
  ['github.com/libp2p/go-yamux/v4', 'v4.0.1'],
]);
const FORBIDDEN_TEXT = [/Hippocratic License/i, /Hippocratic-2\.1/i, /react-leaflet/i, /@react-leaflet\/core/i];
const FORBIDDEN_LICENSE = /(hippocratic|\bjson\b|unknown|unlicensed|commons clause|business source|busl|sspl)/i;

function sha256File(filePath) {
  const hash = createHash('sha256');
  hash.update(fs.readFileSync(filePath));
  return hash.digest('hex');
}

function listFilesRecursive(root) {
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

function assert(condition, message, failures) {
  if (!condition) failures.push(message);
}

function verifyChecksums(complianceRoot, failures) {
  const checksumPath = path.join(complianceRoot, 'CHECKSUMS.sha256');
  if (!fs.existsSync(checksumPath)) return;
  const entries = fs.readFileSync(checksumPath, 'utf8').split(/\r?\n/).filter(Boolean);
  for (const entry of entries) {
    const match = entry.match(/^([0-9a-f]{64})  (.+)$/);
    assert(match, `Malformed checksum entry: ${entry}`, failures);
    if (!match) continue;
    const filePath = path.join(complianceRoot, ...match[2].split('/'));
    assert(fs.existsSync(filePath), `Checksummed file is missing: ${match[2]}`, failures);
    if (fs.existsSync(filePath)) assert(sha256File(filePath) === match[1], `Checksum mismatch: ${match[2]}`, failures);
  }
}

function verifyAssets(bundleRoot, complianceRoot, failures) {
  const inventoryPath = path.join(complianceRoot, 'ASSET-INVENTORY.json');
  if (!fs.existsSync(inventoryPath)) return;
  const inventory = JSON.parse(fs.readFileSync(inventoryPath, 'utf8'));
  assert(Array.isArray(inventory.assets), 'ASSET-INVENTORY.json has no assets array', failures);
  for (const asset of inventory.assets || []) {
    const filePath = path.join(bundleRoot, 'relay', 'web', ...asset.path.split('/'));
    assert(fs.existsSync(filePath), `Inventoried panel asset is missing: ${asset.path}`, failures);
    if (fs.existsSync(filePath)) assert(sha256File(filePath) === asset.sha256, `Panel asset checksum mismatch: ${asset.path}`, failures);
  }
}

function scanForbidden(bundleRoot, failures) {
  const textExtensions = new Set(['.css', '.html', '.js', '.json', '.map', '.md', '.txt', '.yml', '.yaml']);
  for (const file of listFilesRecursive(bundleRoot)) {
    if (!textExtensions.has(path.extname(file).toLowerCase())) continue;
    const text = fs.readFileSync(file, 'utf8');
    for (const pattern of FORBIDDEN_TEXT) {
      assert(!pattern.test(text), `Forbidden non-OSI dependency marker ${pattern} found in ${path.relative(bundleRoot, file)}`, failures);
    }
  }
}

function main() {
  const args = process.argv.slice(2);
  const requireImmutable = args.includes('--require-immutable-sources');
  const positional = args.filter((arg) => !arg.startsWith('--'));
  if (positional.length !== 1) throw new Error('Usage: node verify.mjs <bundle-directory> [--require-immutable-sources]');
  const bundleRoot = path.resolve(positional[0]);
  const complianceRoot = path.join(bundleRoot, 'compliance');
  const failures = [];

  assert(fs.existsSync(bundleRoot), `Bundle does not exist: ${bundleRoot}`, failures);
  assert(fs.existsSync(complianceRoot), `Compliance directory is missing: ${complianceRoot}`, failures);
  if (!fs.existsSync(complianceRoot)) {
    console.error(failures.join('\n'));
    process.exit(1);
  }

  for (const file of REQUIRED_FILES) assert(fs.existsSync(path.join(complianceRoot, file)), `Compliance file is missing: ${file}`, failures);
  for (const component of FIRST_PARTY) {
    const license = path.join(complianceRoot, 'FIRST_PARTY', component, 'LICENSE');
    assert(fs.existsSync(license), `First-party license is missing: ${component}/LICENSE`, failures);
    if (fs.existsSync(license)) assert(/MIT License/.test(fs.readFileSync(license, 'utf8')), `First-party license is not MIT: ${component}`, failures);
  }

  const sourcePath = path.join(complianceRoot, 'SOURCE-MANIFEST.json');
  if (fs.existsSync(sourcePath)) {
    const sources = JSON.parse(fs.readFileSync(sourcePath, 'utf8'));
    assert(Array.isArray(sources.components) && sources.components.length === FIRST_PARTY.length, 'Source manifest does not contain all first-party components', failures);
    for (const component of sources.components || []) {
      assert(component.repository && component.matchingSource, `Source manifest is incomplete for ${component.id}`, failures);
      if (requireImmutable) assert(/^[0-9a-f]{40}$/i.test(component.revision) && component.immutable === true, `Source revision is not immutable for ${component.id}: ${component.revision}`, failures);
    }
  }

  const noticesPath = path.join(complianceRoot, 'THIRD-PARTY-NOTICES.json');
  if (fs.existsSync(noticesPath)) {
    const notices = JSON.parse(fs.readFileSync(noticesPath, 'utf8'));
    assert(Array.isArray(notices.thirdParty) && notices.thirdParty.length > 0, 'Third-party inventory is empty', failures);
    for (const record of notices.thirdParty || []) {
      assert(record.status === 'accepted', `Rejected dependency record: ${record.scope}:${record.name}@${record.version}`, failures);
      assert(!FORBIDDEN_LICENSE.test(record.licenseDeclared || ''), `Forbidden declared license: ${record.name}@${record.version} (${record.licenseDeclared})`, failures);
      assert(!FORBIDDEN_LICENSE.test(record.licenseConcluded || ''), `Forbidden concluded license: ${record.name}@${record.version} (${record.licenseConcluded})`, failures);
      assert(Array.isArray(record.licenseFiles) && record.licenseFiles.length > 0, `No license text recorded for ${record.name}@${record.version}`, failures);
      for (const licenseFile of record.licenseFiles || []) assert(fs.existsSync(path.join(complianceRoot, ...licenseFile.split('/'))), `Referenced license file is missing: ${licenseFile}`, failures);
      const generatedEvidence = (record.licenseFiles || []).some((licenseFile) => /(?:UPSTREAM-(?:LICENSE|README-LICENSE\.md)|CURATED-LICENSE\.txt)$/.test(licenseFile));
      if (generatedEvidence) {
        assert(record.licenseEvidence, `Generated license text has no provenance record: ${record.name}@${record.version}`, failures);
        if (record.licenseEvidence) {
          const provenancePath = path.join(complianceRoot, ...record.licenseEvidence.split('/'));
          assert(fs.existsSync(provenancePath), `License provenance file is missing: ${record.licenseEvidence}`, failures);
          if (fs.existsSync(provenancePath) && (record.licenseFiles || []).some((licenseFile) => /CURATED-LICENSE\.txt$/.test(licenseFile))) {
            try {
              const provenance = JSON.parse(fs.readFileSync(provenancePath, 'utf8'));
              assert(provenance.evidenceKind === 'curated-exact-version-license-completion', `Curated evidence kind is invalid: ${record.name}@${record.version}`, failures);
              assert(provenance.coordinate === `${record.ecosystem}:${record.name}@${record.version}`, `Curated evidence coordinates do not match: ${record.name}@${record.version}`, failures);
              assert(provenance.licenseChoice === record.licenseConcluded, `Curated license choice does not match: ${record.name}@${record.version}`, failures);
              assert(/^https:\/\//.test(provenance.upstreamEvidence || ''), `Curated upstream evidence URL is missing: ${record.name}@${record.version}`, failures);
              assert(/^[0-9a-f]{40}$/i.test(provenance.upstreamRevision || '') && Boolean(provenance.rationale), `Curated evidence is incomplete: ${record.name}@${record.version}`, failures);
              if (record.ecosystem === 'npm') {
                assert(/^sha\d+-/.test(provenance.integrity || '') && /^[0-9a-f]{40}$/i.test(provenance.shasum || ''), `Curated npm archive hashes are missing: ${record.name}@${record.version}`, failures);
                assert(/^https:\/\//.test(provenance.exactArchive || '') && /^https:\/\//.test(provenance.registryMetadata || ''), `Curated npm archive links are missing: ${record.name}@${record.version}`, failures);
              }
              if (record.ecosystem === 'go') {
                assert(/^h1:/.test(provenance.moduleChecksum || '') && /^h1:/.test(provenance.goModChecksum || ''), `Curated Go module checksums are missing: ${record.name}@${record.version}`, failures);
                assert(provenance.sourceRef === provenance.upstreamRevision && provenance.moduleVersion === record.version, `Curated Go origin does not match: ${record.name}@${record.version}`, failures);
              }
            } catch (error) {
              failures.push(`Curated provenance is invalid JSON for ${record.name}@${record.version}: ${error.message}`);
            }
          }
        }
      }
    }
    for (const [name, version] of REQUIRED_MPL) {
      const match = (notices.thirdParty || []).find((record) => record.name === name && record.version === version && /MPL-2\.0/.test(record.licenseConcluded));
      assert(match, `Required MPL component/source record is missing: ${name}@${version}`, failures);
      if (match) assert(match.package && match.package.includes(`@${version}`), `MPL exact source link is missing: ${name}@${version}`, failures);
    }
    const nodeRuntime = (notices.thirdParty || []).find((record) => record.scope === 'node-runtime' && record.name === 'Node.js');
    assert(nodeRuntime && /^\d+\.\d+\.\d+/.test(nodeRuntime.version), 'Exact Node.js runtime license record is missing', failures);
  }

  verifyChecksums(complianceRoot, failures);
  verifyAssets(bundleRoot, complianceRoot, failures);
  scanForbidden(bundleRoot, failures);

  for (const binary of ['hornets-relay', 'airlock', 'hornets-hyperswarm']) {
    const matches = fs.readdirSync(path.join(bundleRoot, 'bin')).filter((name) => name === binary || name === `${binary}.exe`);
    assert(matches.length === 1, `Expected exactly one ${binary} binary`, failures);
  }

  if (failures.length > 0) {
    console.error(`Compliance verification failed:\n- ${failures.join('\n- ')}`);
    process.exit(1);
  }
  console.log(`Compliance verification passed for ${bundleRoot}`);
}

main();
