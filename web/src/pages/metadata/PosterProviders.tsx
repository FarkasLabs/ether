import { Button, Input, Switch, toast } from "@braedonsaunders/appkit-ui";
import { useEffect, useState } from "react";
import { api, apiFetch } from "../../lib/api";
import { MutationError } from "../../components/feedback";
import { nativeSelect } from "./shared";

import type { PosterProviderConfig as Posters } from "../../lib/types";
const empty: Posters = {
  enabled: false,
  source: "postersplus",
  baseUrl: "",
  apiKey: "",
  urlTemplate: "",
};

export function PosterProviders({ onSaved }: { onSaved: () => void }) {
  const [draft, setDraft] = useState<Posters | null>(null);
  const [error, setError] = useState<unknown>(null);
  const [saving, setSaving] = useState(false);
  useEffect(() => {
    void api
      .getConfig()
      .then((c) => setDraft({ ...empty, ...c.posters, source: c.posters?.source || "postersplus" }))
      .catch(setError);
  }, []);
  const save = async () => {
    setSaving(true);
    setError(null);
    try {
      const result = await apiFetch<{ value: Posters }>("/config/posters", {
        method: "PUT",
        body: JSON.stringify(draft),
      });
      setDraft(result.value);
      onSaved();
      toast.success("Poster provider saved");
    } catch (e) {
      setError(e);
    } finally {
      setSaving(false);
    }
  };
  return (
    <section className="metadata-control-section" aria-label="Poster provider">
      <h3>Poster provider</h3>
      <p>
        Use an owner-configured source for movie and show posters. Missing artwork falls back to
        TMDB. Ether badges are applied on top; disable overlapping badges below.
      </p>
      <MutationError error={error} />
      {draft && (
        <div className="space-y-3">
          <label className="flex items-center justify-between">
            Enable poster provider
            <Switch
              aria-label="Enable poster provider"
              checked={draft.enabled}
              onChange={(e) => setDraft({ ...draft, enabled: e.currentTarget.checked })}
            />
          </label>
          <label className="block">
            Source
            <select
              className={nativeSelect}
              value={draft.source || "postersplus"}
              onChange={(e) =>
                setDraft({ ...empty, enabled: draft.enabled, source: e.target.value })
              }
            >
              <option value="postersplus">PostersPlus (self-hosted)</option>
              <option value="betterposters">BetterPosters</option>
              <option value="rpdb">Rating Poster Database</option>
              <option value="custom">Custom URL template</option>
            </select>
          </label>
          {draft.source === "postersplus" && (
            <label className="block">
              Server URL
              <Input
                placeholder="http://postersplus:8000"
                value={draft.baseUrl}
                onChange={(e) => setDraft({ ...draft, baseUrl: e.target.value, apiKey: "" })}
              />
              <small>
                Your server may be on your LAN. Changing its address clears the saved key.
              </small>
            </label>
          )}
          {draft.source !== "betterposters" && (
            <label className="block">
              {draft.source === "postersplus" ? "Access key (optional)" : "API key"}
              <Input
                type="password"
                autoComplete="new-password"
                value={draft.apiKey}
                onChange={(e) => setDraft({ ...draft, apiKey: e.target.value })}
              />
            </label>
          )}
          {(draft.source === "custom" || draft.source === "betterposters") && (
            <label className="block">
              Poster URL template
              <Input
                type="password"
                autoComplete="new-password"
                value={draft.urlTemplate}
                onChange={(e) => setDraft({ ...draft, urlTemplate: e.target.value, apiKey: "" })}
              />
              <small>
                {draft.source === "betterposters"
                  ? "Optional: paste the Poster URL from btttr.cc/configure. Blank uses its default IMDb poster endpoint."
                  : "Use {imdb_id}, or {tmdb_id} with {type} (movie/tv). Optional {api_key} uses the separate key. LAN URLs are allowed; redirects are refused."}
              </small>
            </label>
          )}
          <p>
            Save to update the artwork preview on this page. Plex refreshes its own cached posters
            separately. Seasons and episodes retain TMDB artwork.
          </p>
          <Button disabled={saving} onClick={() => void save()}>
            {saving ? "Saving…" : "Save poster provider"}
          </Button>
        </div>
      )}
    </section>
  );
}
