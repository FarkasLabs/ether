import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import { ProwlarrImport } from "../components/ProwlarrImport";

describe("Prowlarr import limits", () => {
  it("previews and imports without requiring plan quotas or imposing arbitrary caps", async () => {
    const fetch = vi.fn(async (_url: unknown, init?: RequestInit) => {
      const importing = JSON.parse(init?.body as string).import === true;
      return new Response(
        JSON.stringify({
          indexers: [{ id: "test", name: "My indexer", dailyLimit: 0, dailyGrabs: 0 }],
          imported: importing,
          skippedUnknownProtocol: 1,
          warnings: [
            "skipped 1 Prowlarr indexer(s) with an unsupported protocol (only usenet/torrent can be imported)",
          ],
        }),
        { headers: { "Content-Type": "application/json" } },
      );
    });
    vi.stubGlobal("fetch", fetch);
    render(<ProwlarrImport />);
    expect(screen.queryByLabelText(/Daily searches/)).not.toBeInTheDocument();
    expect(screen.queryByLabelText(/Daily downloads/)).not.toBeInTheDocument();
    fireEvent.change(screen.getByLabelText("Prowlarr URL"), {
      target: { value: "http://prowlarr.test:9696" },
    });
    fireEvent.change(screen.getByLabelText("Prowlarr API key"), { target: { value: "test-key" } });
    fireEvent.click(screen.getByRole("button", { name: "Preview indexers" }));
    await screen.findByText("My indexer · Automatic limits");
    // Preview surfaces skipped indexers and the warning behind them.
    expect(screen.getByText(/Skipped 1 indexer with an unsupported protocol/)).toBeInTheDocument();
    expect(screen.getByText("Worth knowing")).toBeInTheDocument();
    expect(
      screen.getByText(/skipped 1 Prowlarr indexer\(s\) with an unsupported protocol/),
    ).toBeInTheDocument();
    // Import is additive: the UI never reports removals.
    expect(screen.queryByText(/Removed \d+ previously imported/)).not.toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Import indexers" }));
    await waitFor(() => expect(fetch).toHaveBeenCalledTimes(2));
    // Import keeps surfacing the skipped/warning diagnostics.
    expect(screen.getByText(/Skipped 1 indexer with an unsupported protocol/)).toBeInTheDocument();
    expect(screen.queryByText(/Removed \d+ previously imported/)).not.toBeInTheDocument();
    expect(
      fetch.mock.calls.map((call) =>
        JSON.parse((call as unknown as [string, RequestInit])[1].body as string),
      ),
    ).toEqual([
      {
        config: {
          url: "http://prowlarr.test:9696",
          apiKey: "test-key",
          dailyLimit: 0,
          dailyGrabs: 0,
          priority: 0,
        },
        import: false,
      },
      {
        config: {
          url: "http://prowlarr.test:9696",
          apiKey: "test-key",
          dailyLimit: 0,
          dailyGrabs: 0,
          priority: 0,
        },
        import: true,
      },
    ]);
  });
});
