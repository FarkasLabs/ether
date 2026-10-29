import { Button, SettingsSection } from "@braedonsaunders/appkit-ui";
import { useEffect, useState } from "react";
import { api, apiFetch } from "../../lib/api";
import type { EtherUser } from "../../lib/types";

type Settings = { enabled: boolean; clientId: string; clientSecret: string };
type Status = {
  connected: boolean;
  needsReconnect?: boolean;
  lastSync?: string;
  warning?: string;
  queued?: number;
  backoffUntil?: number;
};
type Device = {
  userCode: string;
  verificationUri: string;
  verificationUriComplete: string;
  expiresAt: string;
  interval: number;
};

export function SimklIntegration() {
  const [settings, setSettings] = useState<Settings | null>(null);
  const [users, setUsers] = useState<EtherUser[]>([]);
  const [viewer, setViewer] = useState(0);
  const [status, setStatus] = useState<Status | null>(null);
  const [device, setDevice] = useState<Device | null>(null);
  const [message, setMessage] = useState("");
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  useEffect(() => {
    let live = true;
    Promise.all([apiFetch<{ value: Settings }>("/config/simkl"), api.users()]).then(
      ([cfg, list]) => {
        if (live) {
          setSettings(cfg.value);
          setUsers(list);
          setViewer(list[0]?.id ?? 0);
        }
      },
      (e) => {
        if (live) setError((e as Error).message);
      },
    );
    return () => {
      live = false;
    };
  }, []);
  useEffect(() => {
    let live = true;
    setDevice(null);
    setStatus(null);
    setMessage("");
    if (viewer)
      apiFetch<Status>(`/simkl/${viewer}/status`).then(
        (s) => {
          if (live) setStatus(s);
        },
        (e) => {
          if (live) setError((e as Error).message);
        },
      );
    return () => {
      live = false;
    };
  }, [viewer]);
  const refresh = async () => setStatus(await apiFetch<Status>(`/simkl/${viewer}/status`));
  const act = async (fn: () => Promise<void>) => {
    setBusy(true);
    setError("");
    setMessage("");
    try {
      await fn();
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setBusy(false);
    }
  };
  const post = <T,>(path: string) => apiFetch<T>(`/simkl/${viewer}/${path}`, { method: "POST" });
  const verification = (() => {
    if (!device) return "";
    try {
      const u = new URL(device.verificationUriComplete || device.verificationUri);
      return u.protocol === "https:" && u.hostname === "simkl.com" ? u.href : "";
    } catch {
      return "";
    }
  })();
  return (
    <SettingsSection
      title="Simkl"
      description="Connect a viewer's Simkl account to import watched history and send completed playback."
    >
      {error && (
        <p role="alert" className="text-sm text-danger">
          {error}
        </p>
      )}
      {message && (
        <p role="status" className="text-sm">
          {message}
        </p>
      )}
      {settings && (
        <div className="space-y-3">
          <p className="text-sm text-fg-muted">
            Register your own Simkl AUTH V2 app. A device app needs a client ID; server app
            registrations also need a client secret.
          </p>
          <label className="block">
            <input
              type="checkbox"
              checked={settings.enabled}
              onChange={(e) => setSettings({ ...settings, enabled: e.target.checked })}
            />{" "}
            Enable Simkl
          </label>
          <label className="block">
            Client ID{" "}
            <input
              className="w-full rounded border border-border bg-surface px-2 py-1"
              value={settings.clientId}
              onChange={(e) => setSettings({ ...settings, clientId: e.target.value })}
            />
          </label>
          <label className="block">
            Client secret{" "}
            <input
              className="w-full rounded border border-border bg-surface px-2 py-1"
              type="password"
              value={settings.clientSecret}
              onChange={(e) => setSettings({ ...settings, clientSecret: e.target.value })}
            />
          </label>
          <Button
            disabled={busy}
            onClick={() =>
              void act(async () => {
                await api.putSection("simkl", settings);
                const cfg = await apiFetch<{ value: Settings }>("/config/simkl");
                setSettings(cfg.value);
                setDevice(null);
                setMessage("Simkl settings saved.");
              })
            }
          >
            Save Simkl settings
          </Button>
        </div>
      )}
      <div className="mt-4 space-y-3">
        <label className="block">
          Viewer{" "}
          <select
            disabled={busy}
            value={viewer}
            onChange={(e) => setViewer(Number(e.target.value))}
            className="rounded border border-border bg-surface px-2 py-1"
          >
            {users.map((u) => (
              <option key={u.id} value={u.id}>
                {u.title || u.username}
              </option>
            ))}
          </select>
        </label>
        {status && (
          <p className="text-sm">
            {status.connected
              ? status.needsReconnect
                ? "Reconnect required"
                : "Connected"
              : "Not connected"}
          </p>
        )}
        {status?.queued ? (
          <p className="text-sm">{`Queued history entries: ${status.queued}`}</p>
        ) : null}
        {status?.lastSync && (
          <p className="text-sm text-fg-muted">Last imported activity: {status.lastSync}</p>
        )}
        {status?.backoffUntil && status.backoffUntil > Date.now() ? (
          <p className="text-sm text-fg-muted">{`Sync paused until ${new Date(status.backoffUntil).toLocaleString()}.`}</p>
        ) : null}
        {status?.warning && (
          <p className="text-sm text-fg-muted">
            {status.warning === "auth_revoked"
              ? "Simkl authorization expired or was revoked. Reconnect to continue."
              : status.warning === "queue_full"
                ? "The delivery queue is full; some completions were not queued."
                : status.warning === "anime_mapping"
                  ? "An anime episode was skipped because its numbering could not be verified."
                  : status.warning === "not_found"
                    ? "Simkl could not resolve a history item."
                    : "A history item could not be sent."}
          </p>
        )}
        {device && (
          <div className="space-y-2">
            <p>
              Open the Simkl approval page and enter code <strong>{device.userCode}</strong>.
            </p>
            {verification && (
              <a
                href={verification}
                target="_blank"
                rel="noopener noreferrer"
                className="underline"
              >
                Open Simkl approval
              </a>
            )}
            <p className="text-sm text-fg-muted">
              The code expires at {new Date(device.expiresAt).toLocaleTimeString()}. Wait at least{" "}
              {device.interval} seconds between checks.
            </p>
            <Button
              disabled={busy}
              onClick={() =>
                void act(async () => {
                  const r = await post<{ status: string }>("poll");
                  if (r.status === "authorized") {
                    setDevice(null);
                    await refresh();
                    setMessage("Simkl account connected.");
                  } else setMessage("Waiting for approval.");
                })
              }
            >
              Check connection
            </Button>
          </div>
        )}
        <div className="flex flex-wrap gap-2">
          <Button
            disabled={busy || !viewer || !settings?.enabled}
            onClick={() =>
              void act(async () => {
                setDevice(await post<Device>("start"));
              })
            }
          >
            Connect
          </Button>
          <Button
            disabled={
              busy || !viewer || !status?.connected || status.needsReconnect || !settings?.enabled
            }
            onClick={() =>
              void act(async () => {
                const r = await post<{ imported: number; skipped: number }>("sync");
                await refresh();
                setMessage(`Processed ${r.imported} history entries; skipped ${r.skipped}.`);
              })
            }
          >
            Sync history
          </Button>
          <Button
            disabled={busy || !viewer || (!status?.connected && !device)}
            onClick={() =>
              void act(async () => {
                await post("disconnect");
                setDevice(null);
                await refresh();
                setMessage("Simkl disconnected.");
              })
            }
          >
            Disconnect
          </Button>
        </div>
        <p className="text-sm text-fg-muted">
          Sync keeps existing history and does not remove watched items. Anime episodes without a
          verified mapping are skipped.
        </p>
      </div>
    </SettingsSection>
  );
}
