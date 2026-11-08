import logoUrl from "~/assets/talyvor-logo-small-on-dark.svg";

// The brand files are copied from the logo kit (design/logo/talyvor-logo/svg), never redrawn: the
// wordmark is drawn, so TALYVOR is never set as live text. Docs is dark only, so the on-dark file.

// DocsBrand is the logo with the product named beside it as an eyebrow — the board's product-UI
// sidebar header, as in Track. 120 px wide, the smallest the kit allows the logo.
export function DocsBrand() {
  return (
    <div className="flex items-center gap-2.5">
      <img src={logoUrl} alt="Talyvor" width={120} height={38} className="h-[38px] w-[120px]" />
      <span className="text-[12px] font-medium uppercase tracking-[0.22em] text-label">Docs</span>
    </div>
  );
}
