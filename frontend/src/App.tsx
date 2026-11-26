// App composition: navigation, panes, inspector, task bar, dialogs,
// keyboard shortcuts and drag&drop orchestration.
import { useCallback, useEffect, useRef, useState } from "preact/hooks";
import {
  Entry,
  Job,
  Node,
  Plan,
  RootRef,
  TroveError,
  api,
} from "./api";
import {
  AckDialog,
  ConfirmDeleteDialog,
  JobErrorDialog,
  MixDialog,
  NewFolderDialog,
  PlanDialog,
  RenameDialog,
  ConflictDialog,
} from "./dialogs";
import {
  IconClock,
  IconDisk,
  IconDual,
  IconFolder,
  IconHome,
  IconList,
  IconSettings,
  IconShield,
  IconSingle,
  IconSSD,
  IconStar,
  IconWarn,
  IconFor,
} from "./icons";
import { FilePane, PaneState, newPane } from "./panes";
import { useApp } from "./store";
import { Inspector, InspectorTarget } from "./inspector";
import { TaskBar, TaskCenter } from "./tasks";
import { errText, t } from "./i18n";

type Dialog =
  | { type: "none" }
  | { type: "newFolder"; pane: number }
  | { type: "rename"; pane: number; entry: Entry }
  | { type: "confirmDelete"; pane: number; plan: Plan; count: number; bytes: number; prot: boolean }
  | { type: "plan"; pane: number; plan: Plan; req: { action: string; source?: Node; targetDir?: Node; targets?: Node[]; targetName?: string }; targetLabel: string }
  | { type: "ack"; pane: number; req: Record<string, unknown>; targetLabel: string }
  | { type: "mix"; src: string; dst: string }
  | { type: "conflicts"; jobId: string }
  | { type: "jobError"; jobId: string }
  | { type: "settings" };

interface CtxMenu {
  x: number;
  y: number;
  pane: number;
  entry: Entry | null;
}

export function App() {
  const { caps, shares, devices, prefs, toast, toastErr, clipboard, setClipboard, genId, refreshJobs } = useApp();
  const [panes, setPanes] = useState<PaneState[]>(() => [newPane(null), newPane(null)]);
  const [active, setActive] = useState(0);
  const [dual, setDual] = useState(prefs.dualDefault);
  const [dialog, setDialog] = useState<Dialog>({ type: "none" });
  const [ctx, setCtx] = useState<CtxMenu | null>(null);
  const [inspect, setInspect] = useState<InspectorTarget | null>(null);
  const [taskCenter, setTaskCenter] = useState(false);
  const [view, setView] = useState<"files" | "settings">("files");
  const dragPaths = useRef<{ paths: string[]; from: number } | null>(null);

  const { jobs } = useApp();

  // initial pane roots once roots arrive
  useEffect(() => {
    if (shares.length > 0 && !panes[0].root) {
      patch(0, { root: shares[0], path: "", history: [{ root: shares[0], path: "" }], histIdx: 0 });
      if (dual && shares[1]) {
        patch(1, { root: shares[1], path: "", history: [{ root: shares[1], path: "" }], histIdx: 0 });
      }
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [shares.length]);

  const patch = useCallback(
    (i: number, p: Partial<PaneState>, opts?: { pushHistory?: boolean }) => {
      setPanes((ps) => {
        const cur = ps[i];
        const next = { ...cur, ...p };
        if (opts?.pushHistory) {
          const hist = cur.history.slice(0, cur.histIdx + 1);
          if (hist[hist.length - 1]?.path !== next.path || hist[hist.length - 1]?.root?.id !== next.root?.id) {
            hist.push({ root: next.root!, path: next.path });
            next.history = hist;
            next.histIdx = hist.length - 1;
          }
        }
        const out = [...ps];
        out[i] = next;
        return out;
      });
    },
    [],
  );

  const openEntry = (i: number, e: Entry) => {
    const s = panes[i];
    if (e.kind === "dir") {
      const path = s.path ? `${s.path}/${e.name}` : e.name;
      patch(i, { path }, { pushHistory: true });
      setInspect(null);
    } else if (e.kind === "symlink") {
      toast(errText("UNSUPPORTED_PATH"), "warn");
    } else {
      setInspect({ rootId: `${s.root!.kind}:${s.root!.id}`, path: s.path, entry: e });
    }
  };

  const selectedPaths = (i: number): Entry[] => {
    const s = panes[i];
    return s.entries.filter((e) => s.selection.has(e.name));
  };

  const nodeOf = (i: number): Node | null => {
    const s = panes[i];
    if (!s.root) return null;
    return { root: { kind: s.root.kind, id: s.root.id }, path: s.path };
  };

  // ------------------------------------------------------------------
  // operations
  // ------------------------------------------------------------------

  const doPlan = async (
    i: number,
    req: { action: string; source?: Node; targetDir?: Node; targets?: Node[]; targetName?: string },
    opts: { ack?: boolean; policy?: string } = {},
  ) => {
    try {
      const plan = await api.plan({
        ...req,
        conflictPolicy: opts.policy ?? "ask",
        verify: "size-and-metadata",
        ackProtected: opts.ack,
        clientRequestId: genId(),
      });
      if (!plan.namespaceOk) {
        setDialog({ type: "mix", src: labelOf(req.source), dst: labelOf(req.targetDir) });
        return;
      }
      if (plan.needsAck && !opts.ack) {
        setDialog({ type: "ack", pane: i, req: req as unknown as Record<string, unknown>, targetLabel: labelOf(req.targetDir) });
        return;
      }
      if (req.action === "delete") {
        const targets = req.targets ?? [];
        setDialog({
          type: "confirmDelete",
          pane: i,
          plan,
          count: plan.items,
          bytes: plan.bytes,
          prot: isProtectedTarget(req),
        });
        void targets;
        return;
      }
      setDialog({ type: "plan", pane: i, plan, req, targetLabel: labelOf(req.targetDir) });
    } catch (e) {
      handleOpError(e, req);
    }
  };

  const isProtectedTarget = (req: { targetDir?: Node; targets?: Node[] }): boolean => {
    const ids = ["appdata", "domains", "system", "isos"];
    const refs = [...(req.targets ?? []), ...(req.targetDir ? [req.targetDir] : [])];
    return refs.some((n) => n && ids.includes(n.root.id));
  };

  const labelOf = (n?: Node | null): string => {
    if (!n) return "—";
    return `/${n.root.id}${n.path ? "/" + n.path : ""}`;
  };

  const handleOpError = (e: unknown, req?: { source?: Node; targetDir?: Node }) => {
    if (e instanceof TroveError && e.code === "NAMESPACE_MIX_FORBIDDEN") {
      setDialog({ type: "mix", src: labelOf(req?.source), dst: labelOf(req?.targetDir) });
      return;
    }
    toastErr(e);
  };

  const submitPlan = async (plan: Plan) => {
    try {
      await api.submitJob(plan.id, plan.token!, genId());
      toast(t("msg.jobQueued"));
      refreshJobs();
      setDialog({ type: "none" });
    } catch (e) {
      if (e instanceof TroveError && (e.code === "CONFLICT_REQUIRES_DECISION")) {
        setDialog({ type: "none" });
        refreshJobs();
        return;
      }
      toastErr(e);
    }
  };

  const opPaste = async (i: number) => {
    if (!clipboard || clipboard.paths.length === 0) return;
    const target = nodeOf(i);
    if (!target) return;
    const sourceRoot = clipboard.rootId.split(":");
    const source: Node = { root: { kind: sourceRoot[0] as RootRef["kind"], id: sourceRoot[1] }, path: "" };
    const isMove = clipboard.mode === "cut";
    // Multi-path → plan per top path (V1: submit one job per path)
    for (const p of clipboard.paths) {
      const srcPath = p;
      const req = {
        action: isMove ? "move" : "copy",
        source: { ...source, path: srcPath },
        targetDir: target,
      };
      await doPlan(i, req);
    }
    if (isMove) setClipboard(null);
  };

  const opTransferToOtherPane = async (from: number, action: "copy" | "move", paths: string[]) => {
    const other = 1 - from;
    const target = nodeOf(other);
    if (!target) {
      toast(t("st.noRoots"), "warn");
      return;
    }
    const s = panes[from];
    for (const p of paths) {
      await doPlan(from, {
        action,
        source: { root: { kind: s.root!.kind, id: s.root!.id }, path: p },
        targetDir: target,
      });
    }
  };

  const opDelete = async (i: number, entries: Entry[]) => {
    const s = panes[i];
    if (!s.root || entries.length === 0) return;
    const targets = entries.map((e) => ({
      root: { kind: s.root!.kind, id: s.root!.id },
      path: s.path ? `${s.path}/${e.name}` : e.name,
    }));
    await doPlan(i, { action: "delete", targets });
  };

  const opRename = async (i: number, entry: Entry, newName: string) => {
    const s = panes[i];
    if (!s.root) return;
    try {
      const plan = await api.plan({
        action: "rename",
        source: { root: { kind: s.root.kind, id: s.root.id }, path: s.path ? `${s.path}/${entry.name}` : entry.name },
        targetDir: { root: { kind: s.root.kind, id: s.root.id }, path: s.path },
        targetName: newName,
        clientRequestId: genId(),
      });
      if (plan.conflicts && plan.conflicts.length > 0) {
        toast(errText("CONFLICT_REQUIRES_DECISION"), "warn");
        return;
      }
      await api.execute(plan.id, plan.token!);
      patch(i, {});
      setDialog({ type: "none" });
    } catch (e) {
      handleOpError(e);
    }
  };

  const opMkdir = async (i: number, name: string) => {
    const s = panes[i];
    if (!s.root) return;
    try {
      const plan = await api.plan({
        action: "mkdir",
        targetDir: { root: { kind: s.root.kind, id: s.root.id }, path: s.path },
        targetName: name,
        clientRequestId: genId(),
      });
      await api.execute(plan.id, plan.token!);
      patch(i, {});
      setDialog({ type: "none" });
    } catch (e) {
      toastErr(e);
    }
  };

  // ------------------------------------------------------------------
  // dialog confirmations
  // ------------------------------------------------------------------

  const confirmPlan = async (policy: string) => {
    if (dialog.type !== "plan" && dialog.type !== "confirmDelete" && dialog.type !== "ack") return;
    if (dialog.type === "ack") {
      // re-plan with ack then show final plan dialog
      const req = dialog.req as unknown as Parameters<typeof doPlan>[1];
      setDialog({ type: "none" });
      await doPlan(dialog.pane, req, { ack: true, policy });
      return;
    }
    const plan = dialog.plan;
    if (!plan.token) {
      // needsAck path: re-plan with ack
      const req = "req" in dialog ? (dialog.req as unknown as Parameters<typeof doPlan>[1]) : undefined;
      setDialog({ type: "none" });
      if (req) await doPlan(dialog.pane, req, { ack: true, policy });
      return;
    }
    await submitPlan(plan);
    if (dialog.type === "confirmDelete") patch(dialog.pane, {});
  };

  const resolveConflicts = async (jobId: string, decisions: { path: string; resolution: string }[]) => {
    try {
      await api.resolveJob(jobId, decisions);
      setDialog({ type: "none" });
      refreshJobs();
      toast(t("msg.jobQueued"));
    } catch (e) {
      toastErr(e);
    }
  };

  // ------------------------------------------------------------------
  // keyboard
  // ------------------------------------------------------------------

  useEffect(() => {
    const onKey = (ev: KeyboardEvent) => {
      const el = ev.target as HTMLElement;
      if (el.tagName === "INPUT" || el.tagName === "TEXTAREA" || el.tagName === "SELECT") return;
      const i = active;
      const s = panes[i];
      const mod = ev.ctrlKey || ev.metaKey;
      if (mod && ev.key.toLowerCase() === "c") {
        const sel = selectedPaths(i);
        if (sel.length) {
          setClipboard({ mode: "copy", rootId: `${s.root!.kind}:${s.root!.id}`, paths: sel.map((e) => (s.path ? `${s.path}/${e.name}` : e.name)) });
          toast(t("msg.clipboardSet", { n: sel.length }));
        }
        ev.preventDefault();
      } else if (mod && ev.key.toLowerCase() === "x") {
        const sel = selectedPaths(i);
        if (sel.length) {
          setClipboard({ mode: "cut", rootId: `${s.root!.kind}:${s.root!.id}`, paths: sel.map((e) => (s.path ? `${s.path}/${e.name}` : e.name)) });
          toast(t("msg.clipboardSet", { n: sel.length }));
        }
        ev.preventDefault();
      } else if (mod && ev.key.toLowerCase() === "v") {
        void opPaste(i);
        ev.preventDefault();
      } else if (mod && ev.key.toLowerCase() === "a") {
        const sel = new Set(s.entries.map((e) => e.name));
        patch(i, { selection: sel });
        ev.preventDefault();
      } else if (mod && ev.key.toLowerCase() === "f") {
        (document.activeElement as HTMLElement)?.blur();
        const inputs = document.querySelectorAll<HTMLInputElement>(".tv-filterbox input");
        inputs[active]?.focus();
        ev.preventDefault();
      } else if (ev.key === "F2") {
        const sel = selectedPaths(i);
        if (sel.length === 1) setDialog({ type: "rename", pane: i, entry: sel[0] });
        ev.preventDefault();
      } else if (ev.key === "Delete") {
        const sel = selectedPaths(i);
        if (sel.length) void opDelete(i, sel);
        ev.preventDefault();
      } else if (ev.key === "F5") {
        patch(i, {});
        ev.preventDefault();
      } else if (ev.key === "F6" && dual) {
        const sel = selectedPaths(i);
        if (sel.length) void opTransferToOtherPane(i, ev.shiftKey ? "move" : "copy", sel.map((e) => (s.path ? `${s.path}/${e.name}` : e.name)));
        ev.preventDefault();
      } else if (ev.key === "Backspace") {
        if (s.path) {
          const parent = s.path.split("/").slice(0, -1).join("/");
          patch(i, { path: parent }, { pushHistory: true });
        }
        ev.preventDefault();
      } else if (ev.altKey && ev.key === "ArrowLeft") {
        const s2 = panes[i];
        if (s2.histIdx > 0) {
          const h = s2.history[s2.histIdx - 1];
          patch(i, { path: h.path, histIdx: s2.histIdx - 1 });
        }
        ev.preventDefault();
      } else if (ev.altKey && ev.key === "ArrowRight") {
        const s2 = panes[i];
        if (s2.histIdx < s2.history.length - 1) {
          const h = s2.history[s2.histIdx + 1];
          patch(i, { path: h.path, histIdx: s2.histIdx + 1 });
        }
        ev.preventDefault();
      } else if (ev.key === "Escape") {
        if (ctx) setCtx(null);
      }
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [active, panes, dual, ctx, clipboard]);

  // close context menu on any click elsewhere
  useEffect(() => {
    if (!ctx) return;
    const close = () => setCtx(null);
    window.addEventListener("click", close);
    window.addEventListener("contextmenu", close);
    return () => {
      window.removeEventListener("click", close);
      window.removeEventListener("contextmenu", close);
    };
  }, [ctx]);

  // ------------------------------------------------------------------
  // context menu
  // ------------------------------------------------------------------

  const menuFor = (i: number, entry: Entry | null) => {
    const s = panes[i];
    const sel = selectedPaths(i);
    const one = entry ?? sel[0];
    const multi = sel.length > 1 && (!entry || s.selection.has(entry.name));
    const pathsOf = (es: Entry[]) => es.map((e) => (s.path ? `${s.path}/${e.name}` : e.name));
    return (
      <div class="tv-ctxmenu" style={{ left: ctx!.x, top: ctx!.y }}>
        {entry?.kind === "dir" && (
          <button onClick={() => { openEntry(i, entry); setCtx(null); }}>
            <IconFolder size={14} /> {t("act.openTerminal")}
          </button>
        )}
        {!multi && one && (
          <button onClick={() => { setDialog({ type: "rename", pane: i, entry: one }); setCtx(null); }}>
            <IconPencilI /> {t("act.rename")} <span class="kb">F2</span>
          </button>
        )}
        <button
          disabled={(!multi && !one) || (one && one.kind === "dir")}
          onClick={() => { if (one) setInspect({ rootId: `${s.root!.kind}:${s.root!.id}`, path: s.path, entry: one }); setCtx(null); }}
        >
          <IconInfoI /> {t("act.details")}
        </button>
        {!multi && one && one.kind === "file" && (
          <a
            href={api.downloadUrl(`${s.root!.kind}:${s.root!.id}`, pathsOf([one])[0])}
            style="display:flex;align-items:center;gap:8px;width:100%;padding:6px 10px;border-radius:4px;color:inherit;text-decoration:none"
          >
            <IconDownI /> {t("act.download")}
          </a>
        )}
        <div class="sep" />
        <button
          disabled={!multi && !one}
          onClick={() => { setClipboard({ mode: "copy", rootId: `${s.root!.kind}:${s.root!.id}`, paths: pathsOf(multi ? sel : [one]) }); toast(t("msg.clipboardSet", { n: multi ? sel.length : 1 })); setCtx(null); }}
        >
          <IconCopyI size={14} /> {t("act.copy")} <span class="kb">Ctrl+C</span>
        </button>
        <button
          disabled={!multi && !one}
          onClick={() => { setClipboard({ mode: "cut", rootId: `${s.root!.kind}:${s.root!.id}`, paths: pathsOf(multi ? sel : [one]) }); toast(t("msg.clipboardSet", { n: multi ? sel.length : 1 })); setCtx(null); }}
        >
          <IconMoveI size={14} /> {t("act.cut")} <span class="kb">Ctrl+X</span>
        </button>
        <button
          disabled={!clipboard}
          onClick={() => { void opPaste(i); setCtx(null); }}
        >
          <IconDownI /> {t("act.paste")} <span class="kb">Ctrl+V</span>
        </button>
        {dual && (
          <>
            <div class="sep" />
            <button
              disabled={!multi && !one}
              onClick={() => { void opTransferToOtherPane(i, "copy", pathsOf(multi ? sel : [one])); setCtx(null); }}
            >
              <IconCopyI size={14} /> {t("act.copyToPane")} <span class="kb">F6</span>
            </button>
            <button
              disabled={!multi && !one}
              onClick={() => { void opTransferToOtherPane(i, "move", pathsOf(multi ? sel : [one])); setCtx(null); }}
            >
              <IconMoveI size={14} /> {t("act.moveToPane")} <span class="kb">⇧F6</span>
            </button>
          </>
        )}
        <div class="sep" />
        <button
          class="danger"
          disabled={!multi && !one}
          onClick={() => { void opDelete(i, multi ? sel : [one]); setCtx(null); }}
        >
          <IconTrashI size={14} /> {t("act.delete")} <span class="kb">Del</span>
        </button>
      </div>
    );
  };

  // ------------------------------------------------------------------
  // render
  // ------------------------------------------------------------------

  const needsAttention = jobs.filter((j) => j.state === "needs_attention");

  return (
    <div class="trove">
      <div class="tv-app">
        {/* left nav */}
        <nav class="tv-nav">
          <div class="tv-navgroup">
            <button
              class={"tv-navitem" + (view === "files" ? " active" : "")}
              onClick={() => setView("files")}
            >
              <span class="tv-ic"><IconHome size={15} /></span>
              {t("nav.home")}
            </button>
            <button class="tv-navitem" onClick={() => setTaskCenter(true)}>
              <span class="tv-ic"><IconClock size={15} /></span>
              {t("task.center")}
            </button>
            <button class="tv-navitem" onClick={() => toast("★ " + t("nav.more"), "warn")}>
              <span class="tv-ic"><IconStar size={15} /></span>
              {t("nav.more")}
            </button>
          </div>
          <div class="tv-navgroup">
            <div class="tv-navhead">{t("nav.shares")}</div>
            {shares.map((r) => (
              <button
                key={r.id}
                class={"tv-navitem" + (panes[active].root?.id === r.id && view === "files" ? " active" : "")}
                title={r.protected ? t("nav.protected") : r.path}
                onClick={() => {
                  setView("files");
                  patch(active, { root: { kind: r.kind, id: r.id }, path: "", history: [{ root: { kind: r.kind, id: r.id }, path: "" }], histIdx: 0 });
                }}
              >
                <span class="tv-ic"><IconFolder size={15} /></span>
                <span>
                  {r.id}
                  {r.protected && <span class="tv-sub"> ⚠</span>}
                </span>
              </button>
            ))}
          </div>
          {(caps?.expertMode ?? false) && (
            <div class="tv-navgroup">
              <div class="tv-navhead">{t("nav.devices")}</div>
              {devices.map((r) => (
                <button
                  key={r.kind + r.id}
                  class={"tv-navitem" + (panes[active].root?.id === r.id && view === "files" ? " active" : "")}
                  onClick={() => {
                    setView("files");
                    patch(active, { root: { kind: r.kind, id: r.id }, path: "", history: [{ root: { kind: r.kind, id: r.id }, path: "" }], histIdx: 0 });
                  }}
                >
                  <span class="tv-ic">{r.kind === "disk" ? <IconDisk size={15} /> : <IconSSD size={15} />}</span>
                  <span>
                    {r.id} <span class="tv-sub">{r.fsType}</span>
                  </span>
                </button>
              ))}
            </div>
          )}
          <div class="tv-navgroup">
            <button
              class={"tv-navitem" + (view === "settings" ? " active" : "")}
              onClick={() => setView("settings")}
            >
              <span class="tv-ic"><IconSettings size={15} /></span>
              {t("tb.settings")}
            </button>
          </div>
        </nav>

        {/* workspace */}
        <div class="tv-work">
          {/* header strip */}
          <div style="display:flex;align-items:center;gap:8px;flex:none">
            <button
              class="tv-btn ghost"
              onClick={() => setView(view === "files" ? "settings" : "files")}
            >
              <IconFolder size={14} />
              {view === "files" ? t("app.title") : t("set.title")}
            </button>
            <span class="tv-badge" />
            <span class="tv-spacer" style="flex:1" />
            <div style="display:flex;gap:4px;border:1px solid var(--tv-border-strong);border-radius:6px;padding:2px">
              <button
                class={"tv-btn seg" + (!dual ? " active" : "")}
                style="border:0"
                onClick={() => setDual(false)}
                title={t("tb.single")}
              >
                <IconSingle size={14} />
              </button>
              <button
                class={"tv-btn seg" + (dual ? " active" : "")}
                style="border:0"
                onClick={() => setDual(true)}
                title={t("tb.dual")}
              >
                <IconDual size={14} />
              </button>
            </div>
          </div>

          {view === "settings" ? (
            <SettingsView />
          ) : (
            <div class="tv-panes">
              <FilePane
                state={panes[0]}
                focused={active === 0}
                onFocus={() => setActive(0)}
                onChange={(p, o) => patch(0, p, o)}
                onOpenEntry={(e) => openEntry(0, e)}
                onContext={(ev, e) => setCtx({ x: ev.clientX, y: ev.clientY, pane: 0, entry: e })}
                onSelectionEnd={() => setActive(0)}
                onInspect={(e) =>
                  e && setInspect({ rootId: `${panes[0].root?.kind}:${panes[0].root?.id}`, path: panes[0].path, entry: e })
                }
                onDragStartEntries={(paths) => (dragPaths.current = { paths, from: 0 })}
                dropActive={dual}
                dragIn={dual}
                onDrop={() => {
                  const d = dragPaths.current;
                  if (d && d.from !== 0 && d.paths.length) void opTransferToOtherPane(d.from, "copy", d.paths);
                  dragPaths.current = null;
                }}
              />
              {dual && (
                <FilePane
                  state={panes[1]}
                  focused={active === 1}
                  onFocus={() => setActive(1)}
                  onChange={(p, o) => patch(1, p, o)}
                  onOpenEntry={(e) => openEntry(1, e)}
                  onContext={(ev, e) => setCtx({ x: ev.clientX, y: ev.clientY, pane: 1, entry: e })}
                  onSelectionEnd={() => setActive(1)}
                  onInspect={(e) =>
                    e && setInspect({ rootId: `${panes[1].root?.kind}:${panes[1].root?.id}`, path: panes[1].path, entry: e })
                  }
                  onDragStartEntries={(paths) => (dragPaths.current = { paths, from: 1 })}
                  dropActive={dual}
                  dragIn={dual}
                  onDrop={() => {
                    const d = dragPaths.current;
                    if (d && d.from !== 1 && d.paths.length) void opTransferToOtherPane(d.from, "copy", d.paths);
                    dragPaths.current = null;
                  }}
                />
              )}
              {inspect && view === "files" && (
                <Inspector
                  target={inspect}
                  onClose={() => setInspect(null)}
                  onRename={(e) => setDialog({ type: "rename", pane: active, entry: e })}
                  onDelete={(e) => void opDelete(active, [e])}
                  onMove={(e) => {
                    if (dual) void opTransferToOtherPane(active, "move", [panes[active].path ? `${panes[active].path}/${e.name}` : e.name]);
                    else toast(t("tb.dual"), "warn");
                  }}
                />
              )}
            </div>
          )}

          {view === "files" && (
            <TaskBar
              onOpenCenter={() => setTaskCenter(true)}
              onConflict={(j) => setDialog({ type: "conflicts", jobId: j.id })}
            />
          )}

          {!caps?.writeOk && view === "files" && (
            <div class="tv-alert info" style="flex:none">
              <IconShield />
              <span class="txt">{t("set.readonlyBanner")}</span>
              <button class="tv-btn" onClick={() => setView("settings")}>
                {t("tb.settings")}
              </button>
            </div>
          )}
        </div>
      </div>

      {/* context menu */}
      {ctx && menuFor(ctx.pane, ctx.entry)}

      {/* dialogs */}
      {dialog.type === "newFolder" && (
        <NewFolderDialog
          onClose={() => setDialog({ type: "none" })}
          onCreate={(name) => void opMkdir(dialog.pane, name)}
        />
      )}
      {dialog.type === "rename" && (
        <RenameDialog
          entry={dialog.entry}
          onClose={() => setDialog({ type: "none" })}
          onRename={(name) => void opRename(dialog.pane, dialog.entry, name)}
        />
      )}
      {dialog.type === "confirmDelete" && (
        <ConfirmDeleteDialog
          count={dialog.count}
          bytes={dialog.bytes}
          protectedShare={dialog.prot}
          onClose={() => setDialog({ type: "none" })}
          onConfirm={() => void confirmPlan("skip")}
        />
      )}
      {dialog.type === "plan" && (
        <PlanDialog
          plan={dialog.plan}
          action={dialog.req.action}
          targetLabel={dialog.targetLabel}
          onClose={() => setDialog({ type: "none" })}
          onConfirm={(policy) => void confirmPlan(policy)}
        />
      )}
      {dialog.type === "ack" && (
        <AckDialog
          onClose={() => setDialog({ type: "none" })}
          onAck={() => void confirmPlan("ask")}
        />
      )}
      {dialog.type === "mix" && (
        <MixDialog srcLabel={dialog.src} dstLabel={dialog.dst} onClose={() => setDialog({ type: "none" })} />
      )}
      {dialog.type === "conflicts" && (
        <ConflictsLoader jobId={dialog.jobId} onClose={() => setDialog({ type: "none" })} onResolve={resolveConflicts} />
      )}
      {dialog.type === "jobError" && (
        <JobErrorLoader jobId={dialog.jobId} onClose={() => setDialog({ type: "none" })} onRetry={async (id) => {
          try {
            await api.retryJob(id);
            setDialog({ type: "none" });
            refreshJobs();
          } catch (e) {
            toastErr(e);
          }
        }} />
      )}
      {needsAttention.length > 0 && dialog.type === "none" && !taskCenter && (
        <button
          class="tv-btn danger"
          style="position:absolute;bottom:12px;right:12px;z-index:50"
          onClick={() => setDialog({ type: "conflicts", jobId: needsAttention[0].id })}
        >
          <IconWarn size={14} />
          {t("task.resolveConflicts")} ({needsAttention.length})
        </button>
      )}
      {taskCenter && (
        <TaskCenter
          onClose={() => setTaskCenter(false)}
          onConflict={(j) => {
            setTaskCenter(false);
            setDialog({ type: "conflicts", jobId: j.id });
          }}
        />
      )}

      {/* toasts */}
      <Toasts />
    </div>
  );
}

function ConflictsLoader({
  jobId,
  onClose,
  onResolve,
}: {
  jobId: string;
  onClose: () => void;
  onResolve: (id: string, d: { path: string; resolution: string }[]) => void;
}) {
  const [job, setJob] = useState<Job | null>(null);
  useEffect(() => {
    api.job(jobId).then(setJob).catch(() => setJob(null));
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [jobId]);
  if (!job) return null;
  return <ConflictDialog job={job} onClose={onClose} onResolve={(d) => onResolve(jobId, d)} />;
}

function JobErrorLoader({
  jobId,
  onClose,
  onRetry,
}: {
  jobId: string;
  onClose: () => void;
  onRetry: (id: string) => void;
}) {
  const [job, setJob] = useState<Awaited<ReturnType<typeof api.job>> | null>(null);
  useEffect(() => {
    api.job(jobId).then(setJob).catch(() => setJob(null));
  }, [jobId]);
  if (!job) return null;
  return <JobErrorDialog job={job} onClose={onClose} onRetry={() => onRetry(jobId)} />;
}

function Toasts() {
  const { toasts } = useApp();
  if (toasts.length === 0) return null;
  return (
    <div class="tv-toasts">
      {toasts.map((tt) => (
        <div key={tt.id} class={"tv-toast " + (tt.kind === "err" ? "err" : tt.kind === "warn" ? "warn" : "")}>
          {tt.text}
        </div>
      ))}
    </div>
  );
}

import {
  IconPencil as IconPencilI,
  IconInfo as IconInfoI,
  IconDownload as IconDownI,
  IconCopy as IconCopyI,
  IconMove as IconMoveI,
  IconTrash as IconTrashI,
} from "./icons";
import { SettingsView } from "./settings";

// eslint shims
void FilePane;
void IconList;
void IconFor;
