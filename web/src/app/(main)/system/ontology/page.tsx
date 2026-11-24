"use client";

import * as React from "react";
import { toast } from "sonner";
import { DatabaseIcon, PlayIcon, RefreshCwIcon, SearchIcon } from "lucide-react";

import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Textarea } from "@/components/ui/textarea";
import { Badge } from "@/components/ui/badge";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { api, getToken } from "@/lib/api";
import type { LLMProfile } from "@/lib/types";

type OntologyStatus = {
  url: string;
  reachable: boolean;
  "semantic-compilation/status"?: {
    calculated?: boolean;
    running?: boolean;
    last_success_at?: string | null;
    last_failure_at?: string | null;
  };
  "datasources/dbs"?: { data?: { name?: string; db_type?: string }[] };
};

type SearchHit = {
  id?: string;
  name?: string;
  type?: string;
  kind?: string;
  description?: string;
};

export default function OntologyPage() {
  const [status, setStatus] = React.useState<OntologyStatus | null>(null);
  const [cfg, setCfg] = React.useState({ url: "", env_path: "", embed_model: "" });
  const [cfgLoaded, setCfgLoaded] = React.useState(false);
  const [profiles, setProfiles] = React.useState<LLMProfile[]>([]);
  const [profileId, setProfileId] = React.useState<string>("");
  const [envText, setEnvText] = React.useState("");
  const [envSource, setEnvSource] = React.useState("");
  const [busy, setBusy] = React.useState<string | null>(null);

  const [query, setQuery] = React.useState("");
  const [hits, setHits] = React.useState<SearchHit[]>([]);
  const [termDetail, setTermDetail] = React.useState<Record<string, unknown> | null>(null);

  const [question, setQuestion] = React.useState("");
  const [answerLog, setAnswerLog] = React.useState<string[]>([]);
  const [asking, setAsking] = React.useState(false);

  const refresh = React.useCallback(() => {
    api.ontologyStatus().then((s) => setStatus(s as unknown as OntologyStatus)).catch(() => setStatus(null));
    api.ontologyConfig().then((c) => {
      setCfg({ url: c.url, env_path: c.env_path ?? "", embed_model: c.embed_model ?? "" });
      setCfgLoaded(true);
    }).catch(() => {});
    api.llmProfiles().then(setProfiles).catch(() => {});
  }, []);
  React.useEffect(refresh, [refresh]);

  const saveConfig = async () => {
    setBusy("cfg");
    try {
      await api.ontologySaveConfig({ url: cfg.url, env_path: cfg.env_path, embed_model: cfg.embed_model });
      toast.success("Ontology config saved");
      refresh();
    } catch (e) {
      toast.error(e instanceof Error ? e.message : "save failed");
    } finally {
      setBusy(null);
    }
  };

  const genEnv = async () => {
    setBusy("env");
    try {
      const r = await api.ontologyLLMEnv(profileId ? Number(profileId) : undefined);
      setEnvText(r.env);
      setEnvSource(r.source);
    } catch (e) {
      toast.error(e instanceof Error ? e.message : "env generation failed");
    } finally {
      setBusy(null);
    }
  };

  const writeEnv = async () => {
    setBusy("write");
    try {
      const r = await api.ontologyWriteEnvFile(cfg.env_path || undefined);
      toast.success(`Wrote ${r.written} (${r.source}) — restart the auto-ontology services to apply`);
    } catch (e) {
      toast.error(e instanceof Error ? e.message : "write failed");
    } finally {
      setBusy(null);
    }
  };

  const triggerCompile = async () => {
    setBusy("compile");
    try {
      await api.ontologyProxy("semantic-compilation/trigger", { method: "POST", body: "{}" });
      toast.success("Semantic compilation triggered");
      setTimeout(refresh, 3000);
    } catch (e) {
      toast.error(e instanceof Error ? e.message : "trigger failed");
    } finally {
      setBusy(null);
    }
  };

  const runSearch = async () => {
    if (!query.trim()) return;
    setBusy("search");
    setTermDetail(null);
    try {
      const r = (await api.ontologyProxy("search/global-search", {
        method: "POST",
        body: JSON.stringify({ search_term: query }),
      })) as { data?: SearchHit[] };
      setHits(r.data ?? []);
    } catch (e) {
      toast.error(e instanceof Error ? e.message : "search failed");
    } finally {
      setBusy(null);
    }
  };

  const openTerm = async (id: string) => {
    try {
      const [term, sqlAttrs] = await Promise.all([
        api.ontologyProxy(`terms/${encodeURIComponent(id)}`),
        api.ontologyProxy(`terms/${encodeURIComponent(id)}/sql-attributes`).catch(() => null),
      ]);
      setTermDetail({ term, sql_attributes: sqlAttrs });
    } catch (e) {
      toast.error(e instanceof Error ? e.message : "term fetch failed");
    }
  };

  const ask = async () => {
    if (!question.trim() || asking) return;
    setAsking(true);
    setAnswerLog([]);
    const lines: string[] = [];
    const push = (s: string) => {
      lines.push(s);
      setAnswerLog([...lines]);
    };
    try {
      const token = getToken();
      const resp = await fetch("/api/ontology/proxy/chat/completions", {
        method: "POST",
        headers: {
          "Content-Type": "application/json",
          ...(token ? { Authorization: `Bearer ${token}` } : {}),
        },
        body: JSON.stringify({ question }),
      });
      if (!resp.ok || !resp.body) {
        const t = await resp.text();
        push(`HTTP ${resp.status}: ${t.slice(0, 500)}`);
        return;
      }
      const reader = resp.body.getReader();
      const dec = new TextDecoder();
      let buf = "";
      type ResultEv = { answer?: { response?: string; sql_code?: string } };
      let result: ResultEv | null = null;
      let errMsg = "";
      for (;;) {
        const { done, value } = await reader.read();
        if (done) break;
        buf += dec.decode(value, { stream: true });
        let idx: number;
        while ((idx = buf.indexOf("\n\n")) >= 0) {
          const frame = buf.slice(0, idx);
          buf = buf.slice(idx + 2);
          for (const line of frame.split("\n")) {
            if (!line.startsWith("data:")) continue;
            const data = line.slice(5).trim();
            if (data === "[DONE]") continue;
            try {
              const ev = JSON.parse(data) as { type?: string; label?: string; answer?: { response?: string; sql_code?: string }; message?: string };
              if (ev.type === "step" && ev.label) push(`· ${ev.label}`);
              else if (ev.type === "result" && ev.answer) result = { answer: ev.answer };
              else if (ev.type === "error") errMsg = ev.message ?? "error";
            } catch {
              /* non-JSON frame */
            }
          }
        }
      }
      if (errMsg) push(`ERROR: ${errMsg}`);
      if (result?.answer?.sql_code) push(`SQL:\n${result.answer.sql_code}`);
      if (result?.answer?.response) push(`Answer: ${result.answer.response}`);
      if (!result && !errMsg) push("(stream ended without a result event)");
    } catch (e) {
      push(`request failed: ${e instanceof Error ? e.message : String(e)}`);
    } finally {
      setAsking(false);
    }
  };

  const sem = status?.["semantic-compilation/status"];
  const dbs = status?.["datasources/dbs"]?.data ?? [];

  return (
    <div className="flex flex-col gap-4 p-6">
      <div className="flex items-center justify-between">
        <div>
          <h1 className="text-xl font-semibold">Auto Ontology</h1>
          <p className="text-sm text-muted-foreground">
            Semantic layer sidecar (NVIDIA auto-ontology). Agents use the ontology_* tools; this page proxies its API.
          </p>
        </div>
        <Button variant="outline" size="sm" onClick={refresh}>
          <RefreshCwIcon className="mr-2 h-4 w-4" /> Refresh
        </Button>
      </div>

      <div className="grid gap-4 md:grid-cols-2">
        <Card>
          <CardHeader className="pb-2">
            <CardTitle className="text-base">Connection</CardTitle>
          </CardHeader>
          <CardContent className="flex flex-col gap-3 text-sm">
            <div className="flex items-center gap-2">
              <Badge variant={status?.reachable ? "default" : "destructive"}>
                {status?.reachable ? "reachable" : "unreachable"}
              </Badge>
              <span className="text-muted-foreground">{status?.url ?? "…"}</span>
            </div>
            <div className="text-muted-foreground">
              Semantic layer:{" "}
              {sem?.calculated ? "compiled" : "not compiled"}
              {sem?.running ? " (compiling…)" : ""}
              {sem?.last_success_at ? ` · last success ${new Date(sem.last_success_at).toLocaleString()}` : ""}
            </div>
            <div className="flex items-center gap-2">
              <span className="text-muted-foreground">Databases: {dbs.length}</span>
              {dbs.slice(0, 6).map((d, i) => (
                <Badge key={i} variant="outline">{d.name ?? "?"}</Badge>
              ))}
            </div>
            <Button size="sm" variant="outline" disabled={busy === "compile"} onClick={triggerCompile}>
              <PlayIcon className="mr-2 h-4 w-4" /> Trigger compile
            </Button>
          </CardContent>
        </Card>

        <Card>
          <CardHeader className="pb-2">
            <CardTitle className="text-base">Sidecar config</CardTitle>
          </CardHeader>
          <CardContent className="flex flex-col gap-3">
            <div className="grid gap-1.5">
              <Label htmlFor="ao-url">API URL</Label>
              <Input id="ao-url" value={cfg.url} disabled={!cfgLoaded}
                onChange={(e) => setCfg({ ...cfg, url: e.target.value })} placeholder="http://127.0.0.1:3001" />
            </div>
            <div className="grid gap-1.5">
              <Label htmlFor="ao-env">.env path (for LLM sync)</Label>
              <Input id="ao-env" value={cfg.env_path} disabled={!cfgLoaded}
                onChange={(e) => setCfg({ ...cfg, env_path: e.target.value })} placeholder="/Users/you/auto-ontology/.env" />
            </div>
            <div className="grid gap-1.5">
              <Label htmlFor="ao-embed">Embedding model (optional)</Label>
              <Input id="ao-embed" value={cfg.embed_model} disabled={!cfgLoaded}
                onChange={(e) => setCfg({ ...cfg, embed_model: e.target.value })} placeholder="default per provider" />
            </div>
            <Button size="sm" className="w-fit" disabled={busy === "cfg" || !cfgLoaded} onClick={saveConfig}>
              Save config
            </Button>
          </CardContent>
        </Card>
      </div>

      <Card>
        <CardHeader className="pb-2">
          <CardTitle className="text-base">LLM sync (no NVIDIA NIM)</CardTitle>
        </CardHeader>
        <CardContent className="flex flex-col gap-3">
          <p className="text-sm text-muted-foreground">
            Maps an ARTEX LLM profile (OpenAI-compatible or Ollama) onto the sidecar&apos;s REASONING / NON_REASONING / EMBED endpoints.
            Anthropic-native profiles cannot map — use an OpenAI-compatible profile.
          </p>
          <div className="flex items-end gap-2">
            <div className="grid flex-1 gap-1.5">
              <Label>ARTEX LLM profile</Label>
              <Select value={profileId} onValueChange={setProfileId}>
                <SelectTrigger><SelectValue placeholder="Active profile" /></SelectTrigger>
                <SelectContent>
                  {profiles.map((p) => (
                    <SelectItem key={p.id} value={String(p.id)}>{p.name} ({p.format})</SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </div>
            <Button variant="outline" disabled={busy === "env"} onClick={genEnv}>Generate .env block</Button>
            <Button disabled={busy === "write" || !envText} onClick={writeEnv}>Write to .env</Button>
          </div>
          {envSource && <p className="text-xs text-muted-foreground">source: {envSource}</p>}
          {envText && (
            <Textarea readOnly value={envText} className="font-mono text-xs" rows={12} />
          )}
        </CardContent>
      </Card>

      <div className="grid gap-4 md:grid-cols-2">
        <Card>
          <CardHeader className="pb-2">
            <CardTitle className="text-base">Explore semantics</CardTitle>
          </CardHeader>
          <CardContent className="flex flex-col gap-3">
            <div className="flex gap-2">
              <Input value={query} onChange={(e) => setQuery(e.target.value)}
                onKeyDown={(e) => e.key === "Enter" && runSearch()}
                placeholder="Search terms, tables, columns…" />
              <Button variant="outline" disabled={busy === "search"} onClick={runSearch}>
                <SearchIcon className="h-4 w-4" />
              </Button>
            </div>
            <div className="flex max-h-72 flex-col gap-1 overflow-auto">
              {hits.map((h, i) => (
                <button key={h.id ?? i} onClick={() => h.id && openTerm(h.id)}
                  className="flex items-center gap-2 rounded px-2 py-1 text-left text-sm hover:bg-muted">
                  <Badge variant="outline" className="shrink-0">{h.type ?? h.kind ?? "?"}</Badge>
                  <span className="truncate">{h.name ?? h.id}</span>
                </button>
              ))}
              {hits.length === 0 && <p className="text-sm text-muted-foreground">No results.</p>}
            </div>
            {termDetail && (
              <pre className="max-h-80 overflow-auto rounded bg-muted p-2 text-xs">
                {JSON.stringify(termDetail, null, 2)}
              </pre>
            )}
          </CardContent>
        </Card>

        <Card>
          <CardHeader className="pb-2">
            <CardTitle className="text-base">Ask the ontology</CardTitle>
          </CardHeader>
          <CardContent className="flex flex-col gap-3">
            <div className="flex gap-2">
              <Input value={question} onChange={(e) => setQuestion(e.target.value)}
                onKeyDown={(e) => e.key === "Enter" && ask()}
                placeholder="e.g. revenue by region last quarter" disabled={asking} />
              <Button disabled={asking || !question.trim()} onClick={ask}>
                {asking ? "Running…" : "Ask"}
              </Button>
            </div>
            <div className="flex max-h-96 flex-col gap-1 overflow-auto rounded bg-muted p-3 font-mono text-xs whitespace-pre-wrap">
              {answerLog.length === 0 && <span className="text-muted-foreground">Answer, SQL and steps appear here.</span>}
              {answerLog.map((l, i) => <div key={i}>{l}</div>)}
            </div>
          </CardContent>
        </Card>
      </div>
    </div>
  );
}
