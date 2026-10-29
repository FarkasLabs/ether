import { useAsync, useInterval } from "../../lib/hooks";
import { apiFetch } from "../../lib/api";
import type { FieldGroup } from "../settings/SchemaForm";

export const HomeReleaseSettings: FieldGroup = {
  title: "Movie release window",
  fields: [
    {
      key: "gateMoviesByHomeRelease",
      label: "Wait for digital or physical release",
      kind: "bool",
      help: "Movies appear after the earliest digital or physical release anywhere in the world. If TMDB has no such date, they appear 90 days after theatrical release and remain visible if dates change. Movies awaiting metadata stay hidden while the catalog checks them in the background. This does not verify release quality.",
    },
  ],
};

export function HomeReleaseStatus() {
  const data = useAsync(
    () =>
      apiFetch<{ value: { homeReleaseChecked: number; homeReleasePending: number } }>(
        "/status/catalog",
      ),
    [],
  );
  useInterval(() => void data.reload(), 60000);
  const status = data.data?.value;
  if (!status) return null;
  return (
    <section
      className="rounded-xl border border-border bg-surface p-5"
      aria-label="Movie release metadata"
    >
      <h3 className="text-sm font-semibold">Movie release metadata</h3>
      <p className="mt-2 text-xs text-fg-muted">
        {(status.homeReleaseChecked ?? 0).toLocaleString()} checked ·{" "}
        {(status.homeReleasePending ?? 0).toLocaleString()} awaiting details
      </p>
      <p className="mt-2 text-xs text-fg-muted">
        When enabled, checks run in bounded batches in the background. Missing and future dates are
        rechecked.
      </p>
    </section>
  );
}
