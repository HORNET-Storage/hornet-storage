package core

import (
	"strconv"
	"strings"
)

func renderOperatorSetupPage(token string) string {
	return strings.Replace(operatorSetupPageHTML, "__SETUP_TOKEN__", strconv.Quote(token), 1)
}

const operatorSetupPageHTML = `<!doctype html>
<html lang="en">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <title>HORNETS Relay Operator Setup</title>
  <style>
    :root {
      color-scheme: dark;
      --bg: #081018;
      --panel: #101b27;
      --panel-2: #0c1620;
      --border: #26394d;
      --text: #edf6ff;
      --muted: #9db0c3;
      --accent: #69d889;
      --accent-dark: #092515;
      --warn: #f4c66a;
      --bad: #ff9999;
    }
    * { box-sizing: border-box; }
    body {
      margin: 0;
      background: radial-gradient(circle at 10% 0%, rgba(105,216,137,.13), transparent 42%), var(--bg);
      color: var(--text);
      font-family: Inter, "Segoe UI", system-ui, sans-serif;
    }
    main { max-width: 1080px; margin: 0 auto; padding: 26px 18px 50px; }
    header { display: grid; gap: 12px; margin-bottom: 18px; }
    .eyebrow { color: var(--accent); text-transform: uppercase; letter-spacing: .15em; font-size: 12px; font-weight: 800; }
    h1, h2, h3, p { margin-top: 0; }
    h1 { font-size: clamp(30px, 5vw, 48px); margin-bottom: 0; }
    h2 { font-size: 20px; margin-bottom: 6px; }
    h3 { font-size: 15px; margin-bottom: 8px; }
    p, .hint, label, summary { color: var(--muted); }
    .hero-note { max-width: 820px; line-height: 1.6; }
    .grid { display: grid; gap: 14px; }
    .two { grid-template-columns: repeat(2, minmax(0, 1fr)); }
    .card {
      border: 1px solid var(--border);
      border-radius: 18px;
      padding: 18px;
      background: linear-gradient(180deg, rgba(255,255,255,.025), transparent), var(--panel);
      box-shadow: 0 20px 55px rgba(0,0,0,.2);
    }
    .subcard { border: 1px solid rgba(255,255,255,.06); border-radius: 14px; padding: 14px; background: var(--panel-2); }
    .field { display: grid; gap: 6px; }
    .full { grid-column: 1 / -1; }
    label, .hint { font-size: 13px; line-height: 1.45; }
    input, textarea, select {
      width: 100%;
      border: 1px solid var(--border);
      border-radius: 11px;
      background: #08121c;
      color: var(--text);
      padding: 11px 12px;
      font: inherit;
      outline: none;
    }
    input:focus, textarea:focus, select:focus { border-color: var(--accent); box-shadow: 0 0 0 3px rgba(105,216,137,.12); }
    textarea { min-height: 130px; resize: vertical; font-family: Consolas, monospace; }
    input[type="checkbox"] { width: auto; accent-color: var(--accent); }
    .check { display: flex; align-items: center; gap: 10px; color: var(--text); font-weight: 700; }
    .mono { font-family: Consolas, monospace; }
    .callout { border: 1px solid rgba(105,216,137,.3); background: rgba(105,216,137,.07); border-radius: 14px; padding: 13px 15px; line-height: 1.5; }
    .warning { color: var(--warn); }
    details { margin-top: 14px; }
    summary { cursor: pointer; font-weight: 800; }
    .details-body { margin-top: 14px; }
    .actions { display: flex; gap: 10px; flex-wrap: wrap; margin-top: 16px; }
    button { border: 0; border-radius: 999px; padding: 11px 17px; font: inherit; font-weight: 800; cursor: pointer; }
    button.primary { background: var(--accent); color: var(--accent-dark); }
    button.secondary { background: #26394d; color: var(--text); }
    button:disabled { opacity: .55; cursor: wait; }
    #status { margin-top: 14px; border: 1px solid var(--border); border-radius: 14px; padding: 13px 15px; white-space: pre-wrap; font: 13px/1.5 Consolas, monospace; }
    #status.ok { border-color: rgba(105,216,137,.55); }
    #status.bad { border-color: rgba(255,153,153,.55); color: var(--bad); }
    @media (max-width: 760px) { .two { grid-template-columns: 1fr; } .full { grid-column: auto; } }
  </style>
</head>
<body>
<main>
  <header>
    <div class="eyebrow">Portable relay operator setup</div>
    <h1>Bring the complete relay stack online.</h1>
    <p class="hero-note">This profile configures the public relay, Airlock, and their shared hyperswarm sidecar together. A fresh relay is public by default and UPnP is off. Repository permission events, private-repository isolation, Airlock authorization, signatures, and DAG verification remain enforced independently of this relay-wide admission setting.</p>
    <div class="callout"><strong>Identity is automatic.</strong> Leave the private-key fields blank and the relay will generate its Nostr identity, derive its DHT identity, derive a domain-separated Airlock identity, and create an unrelated shared secret on the server. Secrets are never returned to this page. Back up the generated <span class="mono">relay/config.yaml</span> after setup.</div>
  </header>

  <section class="card">
    <h2>Relay identity and reachability</h2>
    <p class="hint">The bind address controls which network interfaces accept relay traffic. Use 0.0.0.0 for a publicly reachable host, then control external exposure with your firewall or reverse proxy.</p>
    <div class="grid two">
      <div class="field"><label for="relay_name">Relay name</label><input id="relay_name" placeholder="HORNETS Relay"></div>
      <div class="field"><label for="relay_contact">Operator contact</label><input id="relay_contact" placeholder="operator@example.com"></div>
      <div class="field full"><label for="relay_description">Description</label><input id="relay_description" placeholder="A public HORNETS relay and repository host."></div>
      <div class="field full"><label for="relay_icon">Icon URL</label><input id="relay_icon" placeholder="https://example.com/relay-icon.png"></div>
      <div class="field"><label for="relay_bind_address">Relay bind address</label><input id="relay_bind_address" class="mono" placeholder="0.0.0.0"></div>
      <div class="field"><label for="relay_port">Relay base port</label><input id="relay_port" type="number" min="1" max="65530" placeholder="11000"></div>
      <div class="field"><label for="access_mode">Relay admission</label><select id="access_mode"><option value="public">Public</option><option value="invite-only">Invite only</option><option value="only-me">Owner only</option><option value="subscription">Subscription</option></select></div>
      <div class="field"><label for="relay_data_path">Relay data path</label><input id="relay_data_path" class="mono" placeholder="./data"></div>
      <div class="field full"><label class="check" for="relay_upnp"><input id="relay_upnp" type="checkbox"> Enable UPnP port mapping</label><div class="hint warning">Off by default. Enable only when you intentionally want automatic router mappings.</div></div>
    </div>
  </section>

  <section class="card" style="margin-top:14px">
    <h2>Airlock and repositories</h2>
    <p class="hint">Airlock stays on loopback by default. Its relay connection points back to this relay, while repository data is stored separately.</p>
    <div class="grid two">
      <div class="field"><label for="airlock_bind_address">Airlock bind address</label><input id="airlock_bind_address" class="mono" placeholder="127.0.0.1"></div>
      <div class="field"><label for="airlock_port">Airlock base port</label><input id="airlock_port" type="number" min="1" max="65534" placeholder="11006"></div>
      <div class="field"><label for="airlock_relay">Relay address used by Airlock</label><input id="airlock_relay" class="mono" placeholder="127.0.0.1:11000"></div>
      <div class="field"><label for="repository_path">Repository path</label><input id="repository_path" class="mono" placeholder="repositories"></div>
    </div>
  </section>

  <details class="card">
    <summary>Advanced identity, sidecar, and raw overrides</summary>
    <div class="details-body grid two">
      <section class="subcard">
        <h3>Identity overrides</h3>
        <div class="grid">
          <div class="field"><label for="relay_owner_pubkey">Owner public key</label><input id="relay_owner_pubkey" class="mono" placeholder="blank uses the relay public key"></div>
          <div class="field"><label for="relay_private_key">Relay private key</label><input id="relay_private_key" class="mono" type="password" autocomplete="off" placeholder="blank generates or preserves the server identity"></div>
          <div class="field"><label for="relay_secret_key">Relay shared secret</label><input id="relay_secret_key" class="mono" type="password" autocomplete="off" placeholder="blank generates or preserves an independent secret"></div>
          <div class="field"><label for="relay_dht_seed">Relay DHT seed</label><input id="relay_dht_seed" class="mono" type="password" autocomplete="off" placeholder="blank derives it from the relay identity"></div>
          <div class="field"><label for="airlock_private_key">Separate Airlock private key</label><input id="airlock_private_key" class="mono" type="password" autocomplete="off" placeholder="blank uses domain-separated relay-key derivation"></div>
          <div class="field"><label for="relay_service_tag">Service tag</label><input id="relay_service_tag" class="mono" placeholder="hornet-storage-service"></div>
        </div>
      </section>
      <section class="subcard">
        <h3>Shared hyperswarm sidecar</h3>
        <div class="grid">
          <div class="field"><label for="sidecar_address">Sidecar address</label><input id="sidecar_address" class="mono" placeholder="127.0.0.1:9100"></div>
          <div class="field"><label for="sidecar_mode">Sidecar mode</label><select id="sidecar_mode"><option value="persistent">Persistent</option><option value="ephemeral">Ephemeral</option></select></div>
          <div class="field"><label for="sidecar_executable">Sidecar executable override</label><input id="sidecar_executable" class="mono" placeholder="blank uses automatic discovery"></div>
          <div class="field"><label>Airlock config target</label><input id="airlock_config_path" class="mono" readonly></div>
        </div>
      </section>
      <section class="subcard full">
        <h3>Raw configuration overrides</h3>
        <p class="hint">JSON objects are merged after the form values. The server still validates identities, access modes, network addresses, ports, and sidecar modes.</p>
        <div class="grid two">
          <div class="field"><label for="relay_overrides">Relay JSON</label><textarea id="relay_overrides" placeholder='{"logging":{"level":"debug"}}'></textarea></div>
          <div class="field"><label for="airlock_overrides">Airlock JSON</label><textarea id="airlock_overrides" placeholder='{"sidecar":{"logdir":"logs/sidecar"}}'></textarea></div>
        </div>
      </section>
      <section class="subcard full">
        <h3>Payload preview</h3>
        <textarea id="payload_preview" readonly></textarea>
      </section>
    </div>
  </details>

  <section class="card" style="margin-top:14px">
    <div class="actions">
      <button id="validate_button" class="secondary" type="button">Validate</button>
      <button id="apply_button" class="primary" type="button">Apply setup and start</button>
    </div>
    <div id="status">Loading safe defaults...</div>
  </section>
</main>

<script>
  const token = __SETUP_TOKEN__;
  let defaults = { relayConfig: {}, airlockConfig: {}, airlockConfigPath: "" };
  let automaticAirlockRelay = "127.0.0.1:11000";
  const byId = (id) => document.getElementById(id);

  function deepMerge(target, source) {
    for (const [key, value] of Object.entries(source || {})) {
      if (value && typeof value === "object" && !Array.isArray(value)) {
        target[key] = deepMerge(target[key] && typeof target[key] === "object" ? target[key] : {}, value);
      } else {
        target[key] = value;
      }
    }
    return target;
  }

  function parseOverride(id) {
    const text = byId(id).value.trim();
    if (!text) return {};
    const parsed = JSON.parse(text);
    if (!parsed || typeof parsed !== "object" || Array.isArray(parsed)) throw new Error(id + " must contain a JSON object");
    return parsed;
  }

  function setStatus(message, ok = true) {
    const status = byId("status");
    status.textContent = message;
    status.className = ok ? "ok" : "bad";
  }

  function numericValue(id, fallback) {
    const value = byId(id).value.trim();
    return Number(value || fallback);
  }

  function redactPayloadForPreview(payload) {
    const preview = JSON.parse(JSON.stringify(payload));
    const relayIdentity = preview.relayConfig?.relay || {};
    for (const key of ["private_key", "secret_key", "dht_seed", "dht_key", "dht_private_key"]) {
      if (relayIdentity[key]) relayIdentity[key] = "<redacted>";
    }
    if (preview.airlockConfig?.private_key) preview.airlockConfig.private_key = "<redacted>";
    const wallet = preview.relayConfig?.external_services?.wallet;
    if (wallet?.key) wallet.key = "<redacted>";
    return preview;
  }

  function syncAirlockRelayToPort() {
    const input = byId("airlock_relay");
    const current = input.value.trim();
    const next = "127.0.0.1:" + numericValue("relay_port", 11000);
    if (!current || current === automaticAirlockRelay) input.value = next;
    automaticAirlockRelay = next;
  }

  function buildPayload() {
    const relay = JSON.parse(JSON.stringify(defaults.relayConfig || {}));
    const airlock = JSON.parse(JSON.stringify(defaults.airlockConfig || {}));
    relay.relay = relay.relay || {};
    relay.server = relay.server || {};
    relay.allowed_users = relay.allowed_users || {};
    relay.sidecar = relay.sidecar || {};
    airlock.sidecar = airlock.sidecar || {};

    relay.relay.name = byId("relay_name").value.trim();
    relay.relay.description = byId("relay_description").value.trim();
    relay.relay.contact = byId("relay_contact").value.trim();
    relay.relay.icon = byId("relay_icon").value.trim();
    relay.relay.service_tag = byId("relay_service_tag").value.trim() || "hornet-storage-service";
    relay.relay.private_key = byId("relay_private_key").value.trim();
    relay.relay.secret_key = byId("relay_secret_key").value.trim();
    relay.relay.dht_seed = byId("relay_dht_seed").value.trim();
    relay.server.bind_address = byId("relay_bind_address").value.trim() || "0.0.0.0";
    relay.server.port = numericValue("relay_port", 11000);
    relay.server.upnp = byId("relay_upnp").checked;
    relay.server.data_path = byId("relay_data_path").value.trim() || "./data";
    relay.allowed_users.mode = byId("access_mode").value;

    const sidecarAddress = byId("sidecar_address").value.trim() || "127.0.0.1:9100";
    const sidecarMode = byId("sidecar_mode").value || "persistent";
    const sidecarExecutable = byId("sidecar_executable").value.trim();
    relay.sidecar.address = sidecarAddress;
    relay.sidecar.mode = sidecarMode;
    relay.sidecar.executable = sidecarExecutable;

    airlock.bind_address = byId("airlock_bind_address").value.trim() || "127.0.0.1";
    airlock.port = numericValue("airlock_port", 11006);
    airlock.relay = byId("airlock_relay").value.trim() || ("127.0.0.1:" + relay.server.port);
    airlock.repository_path = byId("repository_path").value.trim() || "repositories";
    airlock.private_key = byId("airlock_private_key").value.trim();
    airlock.sidecar.address = sidecarAddress;
    airlock.sidecar.mode = sidecarMode;
    airlock.sidecar.executable = sidecarExecutable;

    deepMerge(relay, parseOverride("relay_overrides"));
    deepMerge(airlock, parseOverride("airlock_overrides"));

    const payload = {
      relayConfig: relay,
      airlockConfig: airlock,
      airlockConfigPath: defaults.airlockConfigPath || "",
      relayOwnerPubkey: byId("relay_owner_pubkey").value.trim()
    };
    byId("payload_preview").value = JSON.stringify(redactPayloadForPreview(payload), null, 2);
    return payload;
  }

  async function request(path, body) {
    const response = await fetch(path, {
      method: "POST",
      headers: { "Content-Type": "application/json", "X-Setup-Token": token },
      body: JSON.stringify(body)
    });
    const result = await response.json().catch(() => ({}));
    return { response, result };
  }

  async function validateSetup() {
    try {
      const payload = buildPayload();
      const { response, result } = await request("/setup/validate", payload);
      setStatus(JSON.stringify({ status: response.status, result }, null, 2), response.ok);
      return { ok: response.ok, payload };
    } catch (error) {
      setStatus(String(error), false);
      return { ok: false, payload: null };
    }
  }

  async function applySetup() {
    const validateButton = byId("validate_button");
    const applyButton = byId("apply_button");
    validateButton.disabled = true;
    applyButton.disabled = true;
    try {
      setStatus("Validating operator configuration...");
      const validation = await validateSetup();
      if (!validation.ok || !validation.payload) return;
      setStatus("Writing protected relay and Airlock configuration...");
      const { response, result } = await request("/setup/apply", validation.payload);
      setStatus(JSON.stringify({ status: response.status, result }, null, 2), response.ok);
      if (response.ok) {
        setTimeout(() => setStatus("Setup complete. The relay is starting and the launcher will start Airlock next."), 450);
      }
    } catch (error) {
      setStatus(String(error), false);
    } finally {
      validateButton.disabled = false;
      applyButton.disabled = false;
    }
  }

  async function loadDefaults() {
    const response = await fetch("/setup/defaults", { cache: "no-store" });
    if (!response.ok) throw new Error("Failed to load setup defaults");
    defaults = await response.json();
    if (defaults.profile !== "operator") throw new Error("The relay did not select the operator setup profile");

    const relay = defaults.relayConfig?.relay || {};
    const server = defaults.relayConfig?.server || {};
    const allowed = defaults.relayConfig?.allowed_users || {};
    const relaySidecar = defaults.relayConfig?.sidecar || {};
    const airlock = defaults.airlockConfig || {};
    const airlockSidecar = airlock.sidecar || {};

    byId("relay_name").value = relay.name || "HORNETS Relay";
    byId("relay_description").value = relay.description || "";
    byId("relay_contact").value = relay.contact || "";
    byId("relay_icon").value = relay.icon || "";
    byId("relay_bind_address").value = server.bind_address || "0.0.0.0";
    byId("relay_port").value = String(server.port || 11000);
    byId("relay_upnp").checked = server.upnp === true;
    byId("relay_data_path").value = server.data_path || "./data";
    byId("access_mode").value = allowed.mode || "public";
    byId("relay_service_tag").value = relay.service_tag || "hornet-storage-service";

    byId("airlock_bind_address").value = airlock.bind_address || "127.0.0.1";
    byId("airlock_port").value = String(airlock.port || 11006);
    automaticAirlockRelay = "127.0.0.1:" + String(server.port || 11000);
    byId("airlock_relay").value = airlock.relay || automaticAirlockRelay;
    byId("repository_path").value = airlock.repository_path || "repositories";
    byId("airlock_config_path").value = defaults.airlockConfigPath || "";

    byId("sidecar_address").value = relaySidecar.address || airlockSidecar.address || "127.0.0.1:9100";
    byId("sidecar_mode").value = relaySidecar.mode || airlockSidecar.mode || "persistent";
    byId("sidecar_executable").value = relaySidecar.executable || airlockSidecar.executable || "";

    buildPayload();
    setStatus("Safe operator defaults loaded. Review the bind address and ports, then apply setup.");
  }

  document.addEventListener("input", () => { try { buildPayload(); } catch (_) {} });
  document.addEventListener("change", () => { try { buildPayload(); } catch (_) {} });
  byId("relay_port").addEventListener("input", () => {
    try {
      syncAirlockRelayToPort();
      buildPayload();
    } catch (_) {}
  });
  byId("validate_button").addEventListener("click", validateSetup);
  byId("apply_button").addEventListener("click", applySetup);
  loadDefaults().catch((error) => setStatus(String(error), false));
</script>
</body>
</html>
`
