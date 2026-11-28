<script lang="ts">
  import type { Field } from "../../bindings/github.com/NDari/rho/core/config";
  import type { ServerModel } from "../../bindings/github.com/NDari/rho/core/provider";
  import FieldInput from "./FieldInput.svelte";
  import { humanTokens } from "../lib/format";
  import { pick, type Tree } from "../lib/config";

  // The form for one model of a provider. Changes are kept here until
  // Save; onsave returns an error message, or "" when it was saved.
  let {
    fields,
    initial,
    placeholders,
    provider,
    providers,
    isNew,
    listModels,
    onsave,
    ondelete,
    oncancel,
  }: {
    fields: Field[];
    initial: Tree;
    placeholders: Tree;
    provider: string;
    providers: string[];
    isNew: boolean;
    listModels: () => Promise<ServerModel[] | string>;
    onsave: (fields: Tree, target: string) => Promise<string>;
    ondelete?: () => Promise<string>;
    oncancel: () => void;
  } = $props();

  // svelte-ignore state_referenced_locally
  let vals = $state<Tree>(pick(initial, fields.map((f) => f.key)));
  // svelte-ignore state_referenced_locally
  let target = $state(provider);
  // svelte-ignore state_referenced_locally
  let advanced = $state(fields.some((f) => f.advanced && initial[f.key] !== undefined));
  let error = $state("");
  let saving = $state(false);
  let server = $state<ServerModel[] | null>(null);
  let listing = $state(false);
  let listError = $state("");

  const shownFields = $derived(fields.filter((f) => !f.advanced || advanced));

  function set(key: string, v: unknown) {
    if (v === null || v === undefined) delete vals[key];
    else vals[key] = v;
  }

  function holder(f: Field): unknown {
    if (f.key === "name") return vals.id ?? placeholders.id;
    return placeholders[f.key] ?? f.default;
  }

  async function list() {
    listing = true;
    listError = "";
    const r = await listModels();
    listing = false;
    if (typeof r === "string") listError = r;
    else server = r;
  }

  function choose(id: string) {
    const m = server?.find((x) => x.id === id);
    if (!m) return;
    if (!vals.id) vals.id = m.id;
    vals.name = m.id;
    if (m.context_window) vals.context_window = m.context_window;
    if (m.max_output) vals.max_output = m.max_output;
    if (m.capabilities) {
      vals.supports_thinking = !!m.thinking;
      vals.supports_images = !!m.images;
    }
  }

  async function save() {
    if (!String(vals.id ?? "").trim()) {
      error = "The model needs an ID.";
      return;
    }
    saving = true;
    error = await onsave($state.snapshot(vals) as Tree, target);
    saving = false;
  }

  async function remove() {
    if (!ondelete) return;
    saving = true;
    error = await ondelete();
    saving = false;
  }
</script>

<div class="editor">
  <div class="head">
    <h3>{isNew ? "New model" : String(initial.id ?? "Model")}</h3>
    <div class="server">
      {#if server}
        <select onchange={(e) => choose((e.target as HTMLSelectElement).value)}>
          <option value="">Fill in from a server model…</option>
          {#each server as m (m.id)}
            <option value={m.id}>{m.display_name ? `${m.display_name} (${m.id})` : m.id}{m.context_window ? ` · ${humanTokens(m.context_window)} ctx` : ""}</option>
          {/each}
        </select>
      {:else}
        <button onclick={() => void list()} disabled={listing}>{listing ? "Asking the server…" : "List the server's models"}</button>
      {/if}
    </div>
  </div>
  {#if listError}<div class="err">{listError}</div>{/if}
  <div class="grid">
    {#each shownFields as f (f.key)}
      <label for={"m-" + f.key}>{f.label}</label>
      <div>
        <FieldInput field={f} id={"m-" + f.key} value={vals[f.key]} placeholder={holder(f)} onchange={(v) => set(f.key, v)} />
        {#if f.description}<div class="desc">{f.description}</div>{/if}
      </div>
    {/each}
    {#if providers.length > 1 && !isNew}
      <label for="m-provider">Provider</label>
      <div>
        <select id="m-provider" bind:value={target}>
          {#each providers as p (p)}<option value={p}>{p}</option>{/each}
        </select>
        <div class="desc">Move the model to another provider.</div>
      </div>
    {/if}
  </div>
  <button class="link" onclick={() => (advanced = !advanced)}>{advanced ? "Hide" : "Show"} advanced settings</button>
  {#if error}<pre class="err">{error}</pre>{/if}
  <div class="actions">
    <button class="primary" onclick={() => void save()} disabled={saving}>Save model</button>
    <button onclick={oncancel} disabled={saving}>Cancel</button>
    <span class="spacer"></span>
    {#if ondelete}<button class="danger" onclick={() => void remove()} disabled={saving}>Remove model</button>{/if}
  </div>
</div>

<style>
  .editor {
    border: 1px solid var(--border);
    border-radius: var(--radius);
    padding: 12px 14px;
    background: var(--bg-soft);
    display: flex;
    flex-direction: column;
    gap: 10px;
  }
  .head {
    display: flex;
    align-items: center;
    gap: 12px;
    flex-wrap: wrap;
  }
  h3 {
    margin: 0;
    font-size: 1em;
    flex: 1;
  }
  .server select {
    max-width: 360px;
  }
  select {
    font: inherit;
    color: inherit;
    background: var(--bg-raised);
    border: 1px solid var(--border);
    border-radius: 6px;
    padding: 5px 6px;
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
  .desc {
    color: var(--fg-muted);
    font-size: 12.5px;
    margin-top: 3px;
    max-width: 560px;
  }
  .link {
    align-self: flex-start;
    border: none;
    background: none;
    color: var(--link);
    padding: 0;
  }
  .err {
    color: var(--err);
    white-space: pre-wrap;
    margin: 0;
    font-family: var(--font);
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
</style>
