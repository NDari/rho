<script lang="ts">
  import { Bridge } from "../../bindings/github.com/NDari/rho/gui/bridge";
  import type { ConfigForm } from "../../bindings/github.com/NDari/rho/gui/bridge";
  import type { Edit, Field } from "../../bindings/github.com/NDari/rho/core/config";
  import type { ServerModel } from "../../bindings/github.com/NDari/rho/core/provider";
  import FieldInput from "./FieldInput.svelte";
  import ModelEditor from "./ModelEditor.svelte";
  import { humanTokens } from "../lib/format";
  import { modelsIn, pick, providerIn, providerNames, type Tree } from "../lib/config";

  // One provider: its settings (global config only) and its models. save
  // writes edits to the file being edited and returns an error message,
  // or "" when they were saved.
  let {
    form,
    project,
    name,
    save,
    onrename,
    onclose,
  }: {
    form: ConfigForm;
    project: boolean;
    name: string | null;
    save: (edits: Edit[]) => Promise<string>;
    onrename: (name: string) => void;
    onclose: () => void;
  } = $props();

  const fields: Field[] = $derived(form.provider_fields ?? []);
  const keys = $derived(fields.map((f) => f.key));
  const file = $derived(project ? form.project?.values : form.global.values);
  const other = $derived(project ? form.global.values : form.project?.values);
  const builtin = $derived(!!name && (form.builtin ?? []).includes(name));
  const inFile = $derived(!!name && !!providerIn(file, name));
  const effective = $derived(name ? providerIn(form.effective, name) : undefined);
  const defaults = $derived(name ? providerIn(form.defaults, name) : undefined);

  // svelte-ignore state_referenced_locally
  let newName = $state(name ?? "");
  // svelte-ignore state_referenced_locally
  let vals = $state<Tree>(pick(name ? providerIn(project ? form.project?.values : form.global.values, name) : undefined, (form.provider_fields ?? []).map((f) => f.key)));
  let advanced = $state(false);
  let error = $state("");
  let saving = $state(false);
  let confirmDelete = $state(false);
  let test: { ok: boolean; text: string; models?: ServerModel[] } | null = $state(null);
  let testing = $state(false);
  // editing is a model's index in the file, -1 for a new one, or null.
  let editing = $state<number | null>(null);

  const key = $derived(name ? form.keys?.[name] : undefined);
  const keyEnv = $derived(String(vals.api_key_env ?? (project ? effective?.api_key_env : defaults?.api_key_env) ?? ""));
  const shownFields = $derived(fields.filter((f) => !f.advanced || advanced || (!project && vals[f.key] !== undefined)));

  function set(k: string, v: unknown) {
    if (v === null || v === undefined) delete vals[k];
    else vals[k] = v;
    test = null;
  }

  function holder(f: Field): unknown {
    if (project) return effective?.[f.key];
    return defaults?.[f.key] ?? f.default;
  }

  // The provider's settings as a test should use them: unsaved changes
  // over the built-in provider's (or, in a project, the global ones).
  function testFields(): Tree {
    return project ? pick(effective, keys) : ($state.snapshot(vals) as Tree);
  }

  async function listModels(): Promise<ServerModel[] | string> {
    try {
      return (await Bridge.TestProvider(name ?? newName.trim(), testFields())) ?? [];
    } catch (err) {
      return err instanceof Error ? err.message : String(err);
    }
  }

  async function runTest() {
    testing = true;
    const r = await listModels();
    testing = false;
    if (typeof r === "string") test = { ok: false, text: r };
    else test = { ok: true, text: r.length ? `Connected: the server offers ${r.length} model${r.length === 1 ? "" : "s"}.` : "Connected, but the server lists no models.", models: r };
  }

  async function saveProvider() {
    const n = newName.trim();
    if (!n) {
      error = "The provider needs a name.";
      return;
    }
    saving = true;
    error = await save([{ kind: "provider", provider: inFile ? (name ?? "") : "", name: n, fields: $state.snapshot(vals) as Tree }]);
    saving = false;
    if (!error && n !== name) onrename(n);
  }

  async function removeProvider() {
    if (!confirmDelete) {
      confirmDelete = true;
      return;
    }
    saving = true;
    error = await save([{ kind: "delete_provider", provider: name ?? "" }]);
    saving = false;
    if (!error) onclose();
  }

  const fileModels = $derived(name ? modelsIn(file, name) : []);
  const otherModels = $derived(name ? modelsIn(other, name) : []);
  const resolved = $derived(name ? modelsIn(form.effective, name) : []);
  const targets = $derived(project ? [name ?? ""] : providerNames(form.effective));

  function modelHolders(id: unknown): Tree {
    const m = resolved.find((x) => x.id === id);
    if (m) return m;
    const out: Tree = {};
    for (const k of ["tool_format", "system_prompt_in", "decode_entities", "stop_sequences"]) {
      if (effective?.[k] !== undefined && effective?.[k] !== null) out[k] = effective[k];
    }
    return out;
  }

  async function saveModel(i: number, fieldsVals: Tree, target: string): Promise<string> {
    const err = await save([{ kind: "model", provider: name ?? "", name: target, index: i, fields: fieldsVals }]);
    if (!err) editing = null;
    return err;
  }

  async function deleteModel(i: number): Promise<string> {
    const err = await save([{ kind: "delete_model", provider: name ?? "", index: i }]);
    if (!err) editing = null;
    return err;
  }
</script>

<div class="provider">
  <div class="top">
    <button class="back" onclick={onclose}>← Providers</button>
    <h2>{name ?? "New provider"}</h2>
    {#if builtin}<span class="badge">built-in</span>{/if}
  </div>

  {#if project}
    <p class="note">
      A project config can add models to this provider, but its settings come from the global config, so a repository can't
      send your API keys elsewhere.
    </p>
  {:else if builtin && !inFile}
    <p class="note">Rho knows this provider. Change a setting only to override the built-in one.</p>
  {/if}

  <div class="grid">
    {#if !project}
      <label for="p-name">Name</label>
      <div>
        <input id="p-name" type="text" spellcheck="false" bind:value={newName} placeholder="for example local" />
        <div class="desc">How the config refers to the provider. Its models keep working when you rename it.</div>
      </div>
    {/if}
    {#each shownFields as f (f.key)}
      <label for={"p-" + f.key}>{f.label}</label>
      <div>
        <FieldInput
          field={f}
          id={"p-" + f.key}
          value={project ? undefined : vals[f.key]}
          placeholder={holder(f)}
          disabled={project}
          onchange={(v) => set(f.key, v)}
        />
        {#if f.key === "api_key_env" && keyEnv}
          {#if name && key?.env === keyEnv && key?.set}
            <div class="ok">{keyEnv} is set.</div>
          {:else if name && key?.env === keyEnv}
            <div class="warn">
              {keyEnv} is not set where the GUI can see it. Variables set only in your shell's startup files don't reach apps
              started from the desktop; set it in your desktop session's environment, or start rho-gui from a terminal.
            </div>
          {/if}
        {/if}
        {#if f.description}<div class="desc">{f.description}</div>{/if}
      </div>
    {/each}
  </div>
  {#if !project}
    <button class="link" onclick={() => (advanced = !advanced)}>{advanced ? "Hide" : "Show"} advanced settings</button>
  {/if}

  <div class="actions">
    {#if !project}
      <button class="primary" onclick={() => void saveProvider()} disabled={saving}>Save provider</button>
    {/if}
    <button onclick={() => void runTest()} disabled={testing}>{testing ? "Testing…" : "Test connection"}</button>
    <span class="spacer"></span>
    {#if !project && inFile}
      <button class="danger" onclick={() => void removeProvider()} disabled={saving}>
        {confirmDelete ? "Click again to remove it and its models" : builtin ? "Reset to built-in" : "Remove provider"}
      </button>
    {/if}
  </div>
  {#if test}<div class={test.ok ? "ok" : "err"}>{test.text}</div>{/if}
  {#if error}<pre class="err">{error}</pre>{/if}

  <h3>Models</h3>
  {#if !name}
    <p class="note">Save the provider first, then add its models.</p>
  {:else}
    <div class="models">
      {#each fileModels as m, i (i)}
        {#if editing === i}
          <ModelEditor
            fields={form.model_fields ?? []}
            initial={m}
            placeholders={modelHolders(m.id)}
            provider={name}
            providers={targets}
            isNew={false}
            {listModels}
            onsave={(v, t) => saveModel(i, v, t)}
            ondelete={() => deleteModel(i)}
            oncancel={() => (editing = null)}
          />
        {:else}
          <button class="model" onclick={() => (editing = i)}>
            <span class="id">{m.id}{form.model === m.id ? " (current)" : ""}</span>
            <span class="detail">
              {m.name && m.name !== m.id ? m.name + " · " : ""}{humanTokens(Number(modelHolders(m.id).context_window ?? 0))} ctx{m.supports_thinking ? " · thinking" : ""}{m.supports_images ? " · images" : ""}
            </span>
          </button>
        {/if}
      {/each}
      {#each otherModels as m, i (i)}
        <div class="model readonly" title={project ? "Edit it in the global config" : "Edit it in the project config"}>
          <span class="id">{m.id}</span>
          <span class="detail">from the {project ? "global" : "project"} config</span>
        </div>
      {/each}
      {#if fileModels.length === 0 && otherModels.length === 0 && editing !== -1}
        <p class="note">No models yet.</p>
      {/if}
      {#if editing === -1}
        <ModelEditor
          fields={form.model_fields ?? []}
          initial={{}}
          placeholders={modelHolders(undefined)}
          provider={name}
          providers={targets}
          isNew={true}
          {listModels}
          onsave={(v) => saveModel(-1, v, name ?? "")}
          oncancel={() => (editing = null)}
        />
      {:else}
        <button class="add" onclick={() => (editing = -1)}>Add a model</button>
      {/if}
    </div>
  {/if}
</div>

<style>
  .provider {
    display: flex;
    flex-direction: column;
    gap: 12px;
  }
  .top {
    display: flex;
    align-items: center;
    gap: 12px;
  }
  h2 {
    margin: 0;
    font-size: 1.15em;
  }
  h3 {
    margin: 10px 0 0;
    font-size: 1em;
  }
  .back {
    border: none;
    background: none;
    color: var(--link);
    padding: 0;
  }
  .badge {
    font-size: 11.5px;
    color: var(--fg-muted);
    border: 1px solid var(--border);
    border-radius: 10px;
    padding: 0 8px;
  }
  .note {
    margin: 0;
    color: var(--fg-muted);
    max-width: 640px;
  }
  .grid {
    display: grid;
    grid-template-columns: minmax(120px, 160px) 1fr;
    gap: 10px 14px;
    align-items: start;
  }
  .grid > label {
    padding-top: 5px;
    font-weight: 500;
  }
  .grid input[type="text"] {
    width: 100%;
    max-width: 420px;
  }
  .desc {
    color: var(--fg-muted);
    font-size: 12.5px;
    margin-top: 3px;
    max-width: 560px;
  }
  .ok {
    color: var(--ok);
    font-size: 12.5px;
    margin-top: 3px;
  }
  .warn {
    color: var(--warn);
    font-size: 12.5px;
    margin-top: 3px;
    max-width: 560px;
  }
  .err {
    color: var(--err);
    white-space: pre-wrap;
    margin: 0;
    font-family: var(--font);
  }
  .link {
    align-self: flex-start;
    border: none;
    background: none;
    color: var(--link);
    padding: 0;
  }
  .actions {
    display: flex;
    gap: 8px;
  }
  .spacer {
    flex: 1;
  }
  .danger {
    color: var(--err);
  }
  .models {
    display: flex;
    flex-direction: column;
    gap: 6px;
  }
  .model {
    display: flex;
    justify-content: space-between;
    gap: 12px;
    text-align: left;
    padding: 7px 12px;
    background: var(--bg-soft);
  }
  .model.readonly {
    border: 1px dashed var(--border);
    border-radius: 6px;
    color: var(--fg-muted);
  }
  .id {
    font-family: var(--mono);
    font-size: 12.5px;
  }
  .detail {
    color: var(--fg-muted);
    font-size: 12.5px;
  }
  .add {
    align-self: flex-start;
  }
</style>
