import { describe, expect, it, vi } from "vitest";
import { render } from "@testing-library/react";

// A page's text reaches the search headline, so a page that contains markup must show it as text.
const stableSetQuery = () => {};
vi.mock("~/hooks/useSearch", async () => {
  const actual = await vi.importActual<typeof import("~/hooks/useSearch")>("~/hooks/useSearch");
  const results = [
    {
      page_id: "p1",
      page_title: "<b>Title</b>",
      space_name: "Ops",
      headline: `<img src=x onerror="alert(1)"><mark onclick="x()">hit</mark> <mark>ok</mark>`,
      source: "fulltext",
      url: "/spaces/s1/pages/p1",
    },
  ];
  return {
    ...actual,
    useSearch: () => ({
      query: "hit",
      setQuery: stableSetQuery,
      debounced: "hit",
      data: { results, total: 1, query: "hit", took_ms: 1 },
      isLoading: false,
      error: null,
    }),
  };
});

import { SearchModal } from "./SearchModal";

describe("search results escape what they show", () => {
  it("renders markup from a page as text and keeps only plain <mark> highlights", () => {
    const { container } = render(
      <SearchModal workspaceId="w1" open onClose={() => {}} onOpenPage={() => {}} />,
    );
    expect(container.querySelector("img, b, [onerror], [onclick]")).toBeNull();
    expect([...container.querySelectorAll("mark")].map((m) => m.textContent)).toEqual(["ok"]);
    expect(container.textContent).toContain(`<img src=x onerror="alert(1)">`);
    expect(container.textContent).toContain("<b>Title</b>");
  });
});
