import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { expect, it, vi } from "vitest";
import { PosterProviders } from "../pages/metadata/PosterProviders";
import { api, apiFetch } from "../lib/api";
vi.mock("../lib/api", () => ({ api: { getConfig: vi.fn() }, apiFetch: vi.fn() }));
it("saves independently and clears keys when changing destination", async () => {
  vi.mocked(api.getConfig).mockResolvedValue({
    posters: {
      enabled: true,
      source: "postersplus",
      baseUrl: "http://lan:8000",
      apiKey: "••••",
      urlTemplate: "",
    },
  } as Awaited<ReturnType<typeof api.getConfig>>);
  vi.mocked(apiFetch).mockResolvedValue({
    value: {
      enabled: true,
      source: "postersplus",
      baseUrl: "http://new:8000",
      apiKey: "",
      urlTemplate: "",
    },
  });
  const saved = vi.fn();
  render(<PosterProviders onSaved={saved} />);
  fireEvent.change(await screen.findByLabelText(/Server URL/), {
    target: { value: "http://new:8000" },
  });
  expect(screen.getByLabelText(/Access key/)).toHaveValue("");
  fireEvent.click(screen.getByRole("button", { name: "Save poster provider" }));
  await waitFor(() => expect(saved).toHaveBeenCalledOnce());
  const [path, opts] = vi.mocked(apiFetch).mock.calls[0]!;
  expect(path).toBe("/config/posters");
  expect(JSON.parse(opts!.body as string)).toMatchObject({
    baseUrl: "http://new:8000",
    apiKey: "",
  });
  fireEvent.change(screen.getByLabelText("Source"), { target: { value: "betterposters" } });
  expect(screen.getByLabelText(/Poster URL template/)).toHaveAttribute("type", "password");
});
