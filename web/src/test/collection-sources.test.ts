import { describe, expect, it } from "vitest";
import { describeSource, isSourceComplete, newSource, sourceMeta } from "../lib/rules";

describe("list sources", () => {
  it("defaults StevenLu to the plain popular list and names variants", () => {
    const s = newSource("stevenlu", "movie");
    expect(s).toEqual({ kind: "stevenlu", list: "movies" });
    expect(isSourceComplete(s)).toBe(true);
    expect(sourceMeta("stevenlu")?.label).toBe("StevenLu popular movies");
    expect(describeSource({ kind: "stevenlu", list: "movies-rottentomatoes-min80" })).toBe(
      "StevenLu: Rotten Tomatoes 80+",
    );
  });
});
