import { render } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { CodeBlock } from "./CodeBlock";

describe("CodeBlock", () => {
  it("renders markup in code as text, never as HTML", () => {
    const code = `<img src=x onerror="alert(1)"> & '"`;
    const { container } = render(<CodeBlock language="html" code={code} />);
    expect(container.querySelector("img")).toBeNull();
    expect(container.querySelector("code")?.textContent).toBe(code);
  });
});
