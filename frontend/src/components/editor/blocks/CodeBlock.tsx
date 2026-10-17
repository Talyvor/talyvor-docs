import { useMemo, type ReactNode } from "react";
import { lowlight } from "../extensions/code-highlight";
import type { RootContent, ElementContent } from "hast";

interface CodeBlockProps {
  language?: string;
  code: string;
}

// Read-only renderer for ProseMirror code_block content. The editor
// itself highlights via decorations; this component is for places
// where we need to render code from page JSON without booting an
// EditorView (page previews, search hit snippets).
export function CodeBlock({ language, code }: CodeBlockProps) {
  const nodes = useMemo(() => {
    if (!code) return null;
    try {
      const tree = language
        ? lowlight.highlight(language, code)
        : lowlight.highlightAuto(code);
      return toReact(tree.children);
    } catch {
      return code;
    }
  }, [language, code]);

  return (
    <pre className="overflow-x-auto rounded-md border border-border bg-surface p-3 text-xs">
      <code className={`hljs language-${language ?? "plaintext"}`}>{nodes}</code>
    </pre>
  );
}

// toReact turns the lowlight HAST tree into React nodes, so code text is
// always rendered as text and never parsed as HTML. Doctype / Comment are
// dropped — lowlight never produces them.
function toReact(nodes: ReadonlyArray<RootContent | ElementContent>): ReactNode[] {
  return nodes.map((n, i) => {
    if (n.type === "text") return n.value;
    if (n.type !== "element") return null;
    const cls = (n.properties?.className as string[] | undefined)?.join(" ");
    return (
      <span key={i} className={cls}>
        {toReact(n.children)}
      </span>
    );
  });
}
