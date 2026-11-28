import {
  Activity,
  Ban,
  BellRing,
  Bot,
  Brain,
  Bug,
  ClipboardList,
  FolderOpen,
  FolderSync,
  LayoutDashboard,
  type LucideIcon,
  MessageSquare,
  Network,
  Database,
  Plug,
  Radio,
  ScrollText,
  Settings2,
  ShieldAlert,
  Sparkles,
  Target,
  Terminal,
  Wrench,
} from "lucide-react";

import { t } from "@/i18n";

export type NavBadge = "new" | "soon";

export interface NavSubItem {
  id: string;
  title: string;
  url: string;
  icon?: LucideIcon;
  badge?: NavBadge;
  disabled?: boolean;
  newTab?: boolean;
  description?: string;
}

interface NavItemBase {
  id: string;
  title: string;
  icon?: LucideIcon;
  badge?: NavBadge;
  disabled?: boolean;
  newTab?: boolean;
  description?: string;
}

export interface NavMainLinkItem extends NavItemBase {
  url: string;
  subItems?: never;
}

export interface NavMainParentItem extends NavItemBase {
  subItems: NavSubItem[];
}

export type NavMainItem = NavMainLinkItem | NavMainParentItem;

export interface NavGroup {
  id: number;
  label?: string;
  items: NavMainItem[];
}

export const sidebarItems: NavGroup[] = [
  {
    id: 1,
    label: t("Functions"),
    items: [
      { id: "dashboard", title: t("Dashboard"), url: "/dashboard", icon: LayoutDashboard, description: t("Live counts, recent activity, and the state of every task at a glance.") },
      { id: "chat", title: t("Chat"), url: "/chat", icon: MessageSquare, description: t("Free-form conversation with the agent; quick exploration without creating a task.") },
      { id: "tasks", title: t("Tasks"), url: "/function/tasks", icon: Target, description: t("Structured engagements: scope, LLM profile, findings, report — one task per target.") },
      { id: "findings", title: t("Findings"), url: "/function/findings", icon: Bug, description: t("Every vulnerability the agent has reported across tasks; triage, verify, and export.") },
      { id: "traffic", title: t("Traffic"), url: "/function/traffic", icon: Activity, description: t("Captured requests and responses from the mitm proxy; replay and rewrite rules.") },
      { id: "commands", title: t("Tool Execution"), url: "/function/commands", icon: Terminal, description: t("History of every tool call the agent made (bash, http, builtins), with inputs and outputs.") },
      { id: "llm-records", title: t("LLM Recording"), url: "/function/llm-records", icon: Radio, description: t("Raw prompt and completion traces per LLM call, for debugging prompts or auditing cost.") },
      { id: "assets", title: t("Assets"), url: "/function/assets", icon: Network, description: t("Discovered hosts, URLs, endpoints, parameters — the knowledge graph the agent builds.") },
      { id: "sync", title: t("Asset Sync"), url: "/function/sync", icon: FolderSync, description: t("Import assets from external recon tools (Nuclei, Nmap, Burp) into a task's scope.") },
      { id: "workspace", title: t("Workspace"), url: "/function/workspace", icon: FolderOpen, description: t("Shared file area the agent reads and writes during a task (uploads, tool output, drafts).") },
    ],
  },
  {
    id: 2,
    label: t("System"),
    items: [
      { id: "llm", title: t("LLM"), url: "/system/llm", icon: Brain, description: t("Provider profiles (Anthropic, OpenAI, Ollama), API keys, retry policy, and health.") },
      { id: "agents", title: t("Agent"), url: "/system/agents", icon: Bot, description: t("Prompt templates and persona definitions for the planner, workers, and judges.") },
      { id: "mcp", title: t("MCP"), url: "/system/mcp", icon: Plug, description: t("Model Context Protocol servers the agent can talk to for extra tools or data sources.") },
      { id: "ontology", title: t("Ontology"), url: "/system/ontology", icon: Database, description: t("Semantic layer that lets the agent search assets by meaning, not string match.") },
      { id: "skills", title: t("Skill"), url: "/system/skills", icon: Sparkles, description: t("Reusable multi-step playbooks the agent can invoke (e.g. SSRF chain, auth bypass).") },
      { id: "tools", title: t("Tools"), url: "/system/tools", icon: Wrench, description: t("Low-level primitives the agent calls directly (http, bash, workspace fs, builtins).") },
      { id: "notify", title: t("Notifications"), url: "/system/notify", icon: BellRing, description: t("Outbound channels (Slack, webhook, email) and which events fire which channel.") },
      { id: "intercept", title: t("Interception Rules"), url: "/system/intercept", icon: ShieldAlert, description: t("Patterns that pause the agent before a dangerous tool call and ask the judge or a human.") },
      { id: "asset-intercept", title: t("Asset Interception"), url: "/system/intercept/assets", icon: Ban, description: t("Per-asset deny rules — scope the agent out of specific hosts or paths.") },
      { id: "approvals", title: t("Approval Records"), url: "/system/intercept/approvals", icon: ClipboardList, description: t("Queue of intercepted calls awaiting human decision, plus the audit trail of past decisions.") },
      { id: "logs", title: t("Logs"), url: "/system/logs", icon: ScrollText, description: t("Live backend log stream with level filter, search, and 'load older' history.") },
      { id: "settings", title: t("System Settings"), url: "/system/settings", icon: Settings2, description: t("Global config: target noise profile, trusted proxies, update checks, data directory.") },
    ],
  },
];
