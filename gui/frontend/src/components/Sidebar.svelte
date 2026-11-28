<script lang="ts">
  import { Bridge } from "../../bindings/github.com/NDari/rho/gui/bridge";
  import type { SessionInfo } from "../../bindings/github.com/NDari/rho/gui/bridge";
  import { shortenHome, time } from "../lib/format";

  // The sessions of this project, newest first, with the settings at the
  // bottom. version changes when the list may have changed.
  let {
    version,
    busy,
    cwd,
    onnew,
    onresume,
    onopen,
    onsettings,
    oncollapse,
  }: {
    version: number;
    busy: boolean;
    cwd: string;
    onnew: () => void;
    onresume: (path: string) => void;
    onopen: () => void;
    onsettings: () => void;
    oncollapse: () => void;
  } = $props();

  let sessions = $state<SessionInfo[]>([]);

  $effect(() => {
    void version;
    void Bridge.Sessions().then(
      (s) => (sessions = s ?? []),
      () => {},
    );
  });
</script>

<aside>
  <div class="top">
    <button class="new" onclick={onnew} disabled={busy} title="Start a new session (/new)">+ New session</button>
    <button class="icon" onclick={oncollapse} title="Hide the sidebar">‹</button>
  </div>
  <button class="project" onclick={onopen} disabled={busy} title="Open another project folder (/open)">
    {shortenHome(cwd)}
  </button>
  <div class="list">
    {#each sessions as s (s.path)}
      <button class="session" class:current={s.current} disabled={busy && !s.current} onclick={() => !s.current && onresume(s.path)} title={s.title}>
        <span class="title">{s.title || "(untitled)"}</span>
        <span class="when">{time(s.modified, true)} · {s.messages} msgs</span>
      </button>
    {:else}
      <div class="none">No sessions yet.</div>
    {/each}
  </div>
  <button class="settings" onclick={onsettings} title="Settings (/config)">⚙ Settings</button>
</aside>

<style>
  aside {
    width: 240px;
    flex-shrink: 0;
    display: flex;
    flex-direction: column;
    border-right: 1px solid var(--border);
    background: var(--bg-soft);
    min-height: 0;
  }
  .top {
    display: flex;
    gap: 6px;
    padding: 10px 10px 6px;
  }
  .new {
    flex: 1;
  }
  .icon {
    border: none;
    background: none;
    color: var(--fg-muted);
    font-size: 16px;
    padding: 0 6px;
  }
  .project {
    margin: 0 10px 6px;
    border: none;
    background: none;
    padding: 2px 0;
    text-align: left;
    color: var(--fg-muted);
    font-size: 12px;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .list {
    flex: 1;
    overflow-y: auto;
    padding: 0 6px;
    display: flex;
    flex-direction: column;
    gap: 1px;
  }
  .session {
    border: none;
    background: none;
    text-align: left;
    padding: 6px 8px;
    display: flex;
    flex-direction: column;
    border-radius: 6px;
    min-width: 0;
  }
  .session:hover:not(:disabled) {
    background: var(--bg-raised);
  }
  .session.current {
    background: var(--bg-raised);
    cursor: default;
  }
  .session .title {
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .session .when {
    color: var(--fg-muted);
    font-size: 11.5px;
  }
  .none {
    color: var(--fg-muted);
    padding: 6px 8px;
  }
  .settings {
    margin: 8px 10px 10px;
    text-align: left;
    border: none;
    background: none;
    padding: 6px 8px;
    border-radius: 6px;
  }
  .settings:hover {
    background: var(--bg-raised);
  }
  button:disabled {
    opacity: 0.55;
    cursor: default;
  }
</style>
