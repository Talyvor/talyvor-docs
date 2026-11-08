import { describe, expect, it } from "vitest";
import { render, screen } from "@testing-library/react";
import { DocsBrand } from "./Brand";

// B29.19, B57.83 — the sidebar and the shared-page header carry the brand: the drawn logo file copied from
// the logo kit (never TALYVOR set in a font) with DOCS beside it as an eyebrow, where the amber "T" tile was.
describe("DocsBrand", () => {
  it("shows the copied Talyvor logo file and names the product", () => {
    render(<DocsBrand />);
    const logo = screen.getByRole("img", { name: "Talyvor" });
    expect(logo.getAttribute("src")).toContain("talyvor-logo-small-on-dark");
    expect(screen.getByText("Docs")).toHaveClass("uppercase", "text-label");
    expect(screen.queryByText("T")).toBeNull();
  });
});
