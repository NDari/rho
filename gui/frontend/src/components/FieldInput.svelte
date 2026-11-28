<script lang="ts">
  import type { Field } from "../../bindings/github.com/NDari/rho/core/config";

  // A control for one config field. value is what the file sets
  // (undefined: not set); placeholder is what applies when it isn't set.
  // onchange gets the new value, or null to unset the key.
  let {
    field,
    value,
    placeholder,
    choices,
    disabled = false,
    id,
    onchange,
  }: {
    field: Field;
    value: unknown;
    placeholder?: unknown;
    choices?: string[];
    disabled?: boolean;
    id?: string;
    onchange: (v: unknown) => void;
  } = $props();

  const opts = $derived(choices ?? field.choices ?? []);
  const set = $derived(value !== undefined && value !== null);

  function shown(v: unknown): string {
    if (v === undefined || v === null || v === "") return "";
    if (Array.isArray(v)) return v.join(", ");
    return String(v);
  }

  function num(s: string, whole: boolean): number | null | undefined {
    s = s.trim();
    if (s === "") return null;
    const n = Number(s);
    if (!Number.isFinite(n) || (whole && !Number.isInteger(n))) return undefined;
    return n;
  }

  let bad = $state(false);

  function numberChange(e: Event) {
    const n = num((e.target as HTMLInputElement).value, field.kind === "int");
    bad = n === undefined;
    if (!bad) onchange(n);
  }

  // Lists, headers and pricing.
  const list = $derived(Array.isArray(value) ? (value as string[]) : []);
  let adding = $state("");
  function addItem() {
    const v = adding.trim();
    if (!v) return;
    adding = "";
    if (!list.includes(v)) onchange([...list, v]);
  }
  function toggleChoice(c: string, on: boolean) {
    const base = set ? list : Array.isArray(placeholder) ? (placeholder as string[]) : [];
    const next = on ? [...base.filter((x) => x !== c), c] : base.filter((x) => x !== c);
    onchange(opts.filter((o) => next.includes(o)));
  }

  const headers = $derived(Object.entries((value as Record<string, string>) ?? {}));
  function setHeader(i: number, k: string, v: string) {
    const rows = headers.slice();
    if (i < rows.length) rows[i] = [k, v];
    else rows.push([k, v]);
    const out: Record<string, string> = {};
    for (const [hk, hv] of rows) if (hk.trim()) out[hk.trim()] = hv;
    onchange(Object.keys(out).length ? out : null);
  }

  const priceKeys = ["input", "output", "cache_read", "cache_write"];
  const prices = $derived((value as Record<string, number>) ?? {});
  function setPrice(k: string, s: string) {
    const n = num(s, false);
    if (n === undefined) return;
    const out: Record<string, number> = { ...prices };
    if (n === null) delete out[k];
    else out[k] = n;
    onchange(Object.keys(out).length ? out : null);
  }
</script>

{#if field.kind === "bool"}
  <label class="switch">
    <input
      {id}
      type="checkbox"
      checked={set ? !!value : !!(placeholder ?? field.default)}
      {disabled}
      onchange={(e) => onchange((e.target as HTMLInputElement).checked)}
    />
    <span>{(set ? !!value : !!(placeholder ?? field.default)) ? "On" : "Off"}</span>
  </label>
{:else if field.kind === "choice"}
  <select {id} {disabled} value={set ? String(value) : ""} onchange={(e) => onchange((e.target as HTMLSelectElement).value || null)}>
    <option value="">{shown(placeholder ?? field.default) ? `Default (${shown(placeholder ?? field.default)})` : "Default"}</option>
    {#each opts as c (c)}<option value={c}>{c}</option>{/each}
    {#if set && !opts.includes(String(value))}<option value={String(value)}>{String(value)}</option>{/if}
  </select>
{:else if field.kind === "int" || field.kind === "number"}
  <input
    {id}
    class="num"
    class:bad
    type="text"
    inputmode={field.kind === "int" ? "numeric" : "decimal"}
    {disabled}
    value={shown(value)}
    placeholder={shown(placeholder ?? field.default)}
    onchange={numberChange}
  />
{:else if field.kind === "list" && opts.length > 0}
  <div class="checks">
    {#each opts as c (c)}
      <label>
        <input
          type="checkbox"
          {disabled}
          checked={(set ? list : Array.isArray(placeholder) ? (placeholder as string[]) : []).includes(c)}
          onchange={(e) => toggleChoice(c, (e.target as HTMLInputElement).checked)}
        />
        {c}
      </label>
    {/each}
  </div>
{:else if field.kind === "list"}
  <div class="chips">
    {#each list as item (item)}
      <span class="chip">
        {item}
        <button class="x" title="Remove" {disabled} onclick={() => onchange(list.filter((x) => x !== item))}>×</button>
      </span>
    {/each}
    <input
      {id}
      class="add"
      {disabled}
      bind:value={adding}
      placeholder="add a name"
      onkeydown={(e) => {
        if (e.key === "Enter") {
          e.preventDefault();
          addItem();
        }
      }}
      onblur={addItem}
    />
  </div>
{:else if field.kind === "headers"}
  <div class="rows">
    {#each [...headers, ["", ""]] as [k, v], i (i)}
      <div class="row">
        <input placeholder="Header" value={k} {disabled} onchange={(e) => setHeader(i, (e.target as HTMLInputElement).value, v)} />
        <input placeholder="Value" value={v} {disabled} onchange={(e) => setHeader(i, k, (e.target as HTMLInputElement).value)} />
      </div>
    {/each}
  </div>
{:else if field.kind === "pricing"}
  <div class="prices">
    {#each priceKeys as k (k)}
      <label>
        <span>{k.replace("_", " ")}</span>
        <input
          class="num"
          type="text"
          inputmode="decimal"
          {disabled}
          value={shown(prices[k])}
          placeholder={shown((placeholder as Record<string, number> | undefined)?.[k])}
          onchange={(e) => setPrice(k, (e.target as HTMLInputElement).value)}
        />
      </label>
    {/each}
  </div>
{:else}
  <input
    {id}
    type="text"
    spellcheck="false"
    autocomplete="off"
    {disabled}
    value={shown(value)}
    placeholder={shown(placeholder ?? field.default)}
    onchange={(e) => onchange((e.target as HTMLInputElement).value.trim() || null)}
  />
{/if}

<style>
  input[type="text"],
  select {
    width: 100%;
    max-width: 420px;
  }
  select {
    font: inherit;
    color: inherit;
    background: var(--bg-raised);
    border: 1px solid var(--border);
    border-radius: 6px;
    padding: 5px 6px;
  }
  .num {
    max-width: 160px;
  }
  .bad {
    border-color: var(--err);
  }
  .switch {
    display: inline-flex;
    gap: 8px;
    align-items: center;
    cursor: pointer;
  }
  .switch span {
    color: var(--fg-muted);
  }
  .checks {
    display: flex;
    flex-wrap: wrap;
    gap: 6px 16px;
  }
  .checks label {
    display: inline-flex;
    gap: 5px;
    align-items: center;
    font-family: var(--mono);
    font-size: 12.5px;
  }
  .chips {
    display: flex;
    flex-wrap: wrap;
    gap: 6px;
    align-items: center;
  }
  .chip {
    display: inline-flex;
    align-items: center;
    gap: 2px;
    padding: 1px 4px 1px 8px;
    border: 1px solid var(--border);
    border-radius: 12px;
    background: var(--bg-soft);
    font-family: var(--mono);
    font-size: 12.5px;
  }
  .x {
    border: none;
    background: none;
    padding: 0 4px;
    color: var(--fg-muted);
  }
  .add {
    width: 140px;
    padding: 3px 6px;
  }
  .rows {
    display: flex;
    flex-direction: column;
    gap: 6px;
    max-width: 420px;
  }
  .row {
    display: grid;
    grid-template-columns: 1fr 1fr;
    gap: 6px;
  }
  .prices {
    display: grid;
    grid-template-columns: repeat(4, minmax(0, 110px));
    gap: 8px;
  }
  .prices label {
    display: flex;
    flex-direction: column;
    gap: 2px;
    font-size: 12px;
    color: var(--fg-muted);
  }
  .prices .num {
    max-width: none;
    width: 100%;
  }
</style>
