<script lang="ts">
  import { onMount } from "svelte";
  import { Bridge } from "../../bindings/github.com/NDari/rho/gui/bridge";
  import type { ConfigForm } from "../../bindings/github.com/NDari/rho/gui/bridge";
  import type { Edit, Field } from "../../bindings/github.com/NDari/rho/core/config";
  import FieldInput from "./FieldInput.svelte";
  import ProviderEditor from "./ProviderEditor.svelte";
  import { shortenHome } from "../lib/format";
  import { get, modelsIn, providerIn, providerNames, shown, type Tree } from "../lib/config";

  // The settings window: forms over the global and project config files.
  // onsaved runs after each saved change, so the app can pick it up.
  let { onclose, onsaved }: { onclose: () => void; onsaved: (keys: string[]) => void } = $props();

  let form = $state<ConfigForm | null>(null);
  let loadError = $state("");
  let project = $state(false);
  let section = $state("General");
  // provider is the provider being edited: a name, "" for a new one, or
  // null for the list.
  let provider = $state<string | null>(null);
  let status: { ok: boolean; text: string } | null = $state(null);
  let busyKey = $state("");

  async function load() {
    try {
      form = await Bridge.ConfigForm();
      loadError = "";
    } catch (err) {
      loadError = errText(err);
    }
  }

  function errText(err: unknown): string {
    if (err && typeof err === "object" && "message" in err) return String((err as Error).message);
    return String(err);
  }

  onMount(() => void load());

  const file = $derived(project ? form?.project : form?.global);
  const values = $derived((file?.values ?? {}) as Tree);
  const other = $derived((project ? form?.global.values : form?.project?.values) as Tree | undefined);
  const sections = $derived(["General", "Providers", ...(form?.sections ?? []).filter((s) => s !== "General")]);
  const fieldsIn = $derived((s: string): Field[] => (form?.fields ?? []).filter((f) => f.section === s));
  const modelIDs = $derived(
    providerNames(form?.effective).flatMap((p) => modelsIn(form?.effective, p).map((m) => String(m.id))),
  );

  // save writes edits to the file being edited, then reloads the form.
  // It returns an error message, or "".
  async function save(edits: Edit[]): Promise<string> {
    if (!form || !file) return "";
    try {
      const note = await Bridge.SaveConfig(project, file.version ?? "", edits);
      status = { ok: true, text: "Saved to " + shortenHome(file.path) + (note ? ". " + note : "") };
      await load();
      onsaved(edits.map((e) => e.key ?? e.kind));
      return "";
    } catch (err) {
      const text = errText(err);
      if (text.includes("changed since it was read")) await load();
      status = null;
      return text;
    }
  }

  async function setField(f: Field, v: unknown) {
    busyKey = f.key;
    const err = await save([{ kind: "set", key: f.key, value: v ?? null }]);
    if (err) status = { ok: false, text: err };
    busyKey = "";
  }

  function choicesFor(f: Field): string[] | undefined {
    if (f.key === "default_model") return modelIDs;
    if (f.key === "theme") return ["system", ...(form?.themes ?? [])];
    if (f.key === "theme_dark" || f.key === "theme_light") return form?.themes ?? [];
    return undefined;
  }

  // holder is what applies when the file being edited doesn't set f: the
  // global value under a project, or the built-in default.
  function holder(f: Field): unknown {
    const g = project ? get(form?.global.values, f.key) : undefined;
    if (g !== undefined && g !== null) return g;
    const d = get(form?.defaults, f.key);
    return d ?? f.default;
  }

  function keyText(name: string): string {
    const k = form?.keys?.[name];
    if (k?.raw) return "key stored in the file";
    if (!k?.env) return "no API key";
    return k.set ? `${k.env} is set` : `${k.env} is not set`;
  }

  async function openFile() {
    try {
      await Bridge.OpenConfigFile(project);
    } catch (err) {
      status = { ok: false, text: errText(err) };
    }
  }

  function key(e: KeyboardEvent) {
    if (e.key !== "Escape") return;
    // Esc in a field leaves the field first.
    if (e.target instanceof HTMLInputElement || e.target instanceof HTMLSelectElement) {
      e.target.blur();
    } else {
      onclose();
    }
    e.preventDefault();
  }
</script>

<svelte:window onkeydown={key} />

<div class="settings">
  <header>
    <h1>Settings</h1>
    {#if form?.project}
      <div class="scope" role="tablist">
        <button role="tab" aria-selected={!project} class:on={!project} onclick={() => ((project = false), (provider = null), (status = null))}>Global</button>
        <button role="tab" aria-selected={project} class:on={project} onclick={() => ((project = true), (provider = null), (status = null))}>This project</button>
      </div>
    {/if}
    <span class="spacer"></span>
    <button class="close" onclick={onclose} title="Close (Esc)">×</button>
  </header>

  {#if loadError}
    <pre class="err pad">{loadError}</pre>
  {:else if form && file}
    <div class="body">
      <nav>
        {#each sections as s (s)}
          <button class:on={section === s} onclick={() => ((section = s), (provider = null), (status = null))}>{s}</button>
        {/each}
        <span class="spacer"></span>
        <button class="file" onclick={() => void openFile()}>Open the file</button>
      </nav>
      <div class="content">
        <div class="where">
          {#if project}
            Editing <code>{shortenHome(file.path)}</code>, which applies in {shortenHome(form.project_root ?? "")} and wins over the global
            config.
            {#if form.project_in_git && !form.project_ignored}
              <div class="warn">Git doesn't ignore this file, so changes may be shared with everyone who works on the repository.</div>
            {:else if form.project_in_git}
              <div class="muted">Git ignores this file, so it stays on this machine.</div>
            {/if}
          {:else}
            Editing <code>{shortenHome(file.path)}</code>, which applies everywhere. Its comments are kept.
          {/if}
          {#if !form.running}<div class="warn">Rho isn't running yet; it tries again when you close the settings.</div>{/if}
        </div>
        {#if form.error}<pre class="err">The config doesn't load: {form.error}</pre>{/if}
        {#if file.error}<pre class="err">{file.error}</pre>{/if}
        {#if status}<div class={status.ok ? "ok" : "err"}>{status.text}</div>{/if}

        {#if section === "Providers"}
          {#if provider !== null}
            {#key provider + String(project)}
              <ProviderEditor
                {form}
                {project}
                name={provider || null}
                {save}
                onrename={(n) => (provider = n)}
                onclose={() => (provider = null)}
              />
            {/key}
          {:else}
            <div class="cards">
              {#each providerNames(form.effective) as p (p)}
                {@const pv = providerIn(form.effective, p)}
                {@const models = modelsIn(form.effective, p)}
                <button class="card" onclick={() => (provider = p)}>
                  <div class="name">
                    {p}
                    {#if (form.builtin ?? []).includes(p)}<span class="badge">built-in</span>{/if}
                    {#if providerIn(values, p)}<span class="badge">{project ? "in this project" : "configured"}</span>{/if}
                  </div>
                  <div class="detail">{pv?.type} · {pv?.base_url}</div>
                  <div class="detail">
                    {models.length ? models.map((m) => m.id).join(", ") : "no models"} · {keyText(p)}
                  </div>
                </button>
              {/each}
            </div>
            {#if !project}
              <button class="primary add" onclick={() => (provider = "")}>Add a provider</button>
            {/if}
          {/if}
        {:else}
          <div class="fields">
            {#each fieldsIn(section) as f (f.key)}
              {@const v = get(values, f.key)}
              {@const locked = project && f.global_only}
              {@const over = !project ? get(other, f.key) : undefined}
              <div class="field">
                <label for={"f-" + f.key}>{f.label}</label>
                <div class="control">
                  <FieldInput
                    field={f}
                    id={"f-" + f.key}
                    value={locked ? undefined : v}
                    placeholder={holder(f)}
                    choices={choicesFor(f)}
                    disabled={locked || busyKey === f.key}
                    onchange={(nv) => void setField(f, nv)}
                  />
                  {#if v !== undefined && v !== null && !locked}
                    <button class="reset" onclick={() => void setField(f, null)} title="Remove it from this file">
                      Use {project ? "the global value" : "the default"}
                    </button>
                  {/if}
                </div>
                <div class="desc">
                  {f.description}
                  {#if locked}<div class="muted">Only the global config can set this, so a repository can't change it.</div>{/if}
                  {#if over !== undefined && over !== null}
                    <div class="warn">
                      This project sets it to {shown(over)}, which wins here.
                      <button class="inline" onclick={() => (project = true)}>Edit the project's value</button>
                    </div>
                  {/if}
                </div>
              </div>
            {/each}
            {#if section === "Terminal"}
              <p class="muted">Key bindings are edited in the file (<code>tui.keybindings</code>).</p>
            {/if}
          </div>
        {/if}
      </div>
    </div>
  {:else}
    <div class="pad muted">Loading…</div>
  {/if}
</div>

<style>
  .settings {
    position: fixed;
    inset: 0;
    z-index: 20;
    background: var(--bg);
    display: flex;
    flex-direction: column;
  }
  header {
    display: flex;
    align-items: center;
    gap: 16px;
    padding: 10px 16px;
    border-bottom: 1px solid var(--border);
    background: var(--bg-soft);
  }
  h1 {
    margin: 0;
    font-size: 1.1em;
  }
  .scope {
    display: inline-flex;
    border: 1px solid var(--border);
    border-radius: 6px;
    overflow: hidden;
  }
  .scope button {
    border: none;
    border-radius: 0;
    background: none;
    padding: 3px 12px;
  }
  .scope button.on {
    background: var(--accent);
    color: var(--accent-fg);
  }
  .spacer {
    flex: 1;
  }
  .close {
    border: none;
    background: none;
    font-size: 20px;
    line-height: 1;
    padding: 0 4px;
    color: var(--fg-muted);
  }
  .body {
    flex: 1;
    display: flex;
    min-height: 0;
  }
  nav {
    width: 170px;
    flex-shrink: 0;
    display: flex;
    flex-direction: column;
    gap: 2px;
    padding: 12px 8px;
    border-right: 1px solid var(--border);
  }
  nav button {
    text-align: left;
    border: none;
    background: none;
    padding: 5px 10px;
  }
  nav button.on {
    background: var(--bg-raised);
    font-weight: 600;
  }
  nav .file {
    color: var(--link);
  }
  .content {
    flex: 1;
    overflow-y: auto;
    padding: 16px 24px 40px;
    display: flex;
    flex-direction: column;
    gap: 14px;
    max-width: 980px;
  }
  .where {
    color: var(--fg-muted);
  }
  .fields {
    display: flex;
    flex-direction: column;
    gap: 18px;
  }
  .field {
    display: grid;
    grid-template-columns: minmax(140px, 190px) 1fr;
    gap: 4px 16px;
  }
  .field label {
    font-weight: 500;
    padding-top: 5px;
  }
  .control {
    display: flex;
    align-items: center;
    gap: 10px;
    flex-wrap: wrap;
  }
  .desc {
    grid-column: 2;
    color: var(--fg-muted);
    font-size: 12.5px;
    max-width: 600px;
  }
  .reset,
  .inline {
    border: none;
    background: none;
    color: var(--link);
    padding: 0;
    font-size: 12.5px;
  }
  .cards {
    display: grid;
    grid-template-columns: repeat(auto-fill, minmax(280px, 1fr));
    gap: 10px;
  }
  .card {
    text-align: left;
    padding: 10px 12px;
    background: var(--bg-soft);
    display: flex;
    flex-direction: column;
    gap: 3px;
    min-width: 0;
  }
  .card .name {
    font-weight: 600;
    display: flex;
    gap: 8px;
    align-items: center;
  }
  .card .detail {
    color: var(--fg-muted);
    font-size: 12.5px;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .badge {
    font-weight: normal;
    font-size: 11px;
    color: var(--fg-muted);
    border: 1px solid var(--border);
    border-radius: 10px;
    padding: 0 7px;
  }
  .add {
    align-self: flex-start;
  }
  .ok {
    color: var(--ok);
  }
  .warn {
    color: var(--warn);
  }
  .err {
    color: var(--err);
    white-space: pre-wrap;
    margin: 0;
    font-family: var(--font);
  }
  .muted {
    color: var(--fg-muted);
  }
  .pad {
    padding: 20px;
  }
</style>
