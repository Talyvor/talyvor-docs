import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Mail } from "lucide-react";
import { notificationApi, type NotificationPrefs } from "~/api/notifications";

const KINDS: { kind: keyof NotificationPrefs; label: string; hint: string }[] = [
  {
    kind: "approval.requested",
    label: "Approval requests",
    hint: "An email when someone asks you to approve a page.",
  },
  {
    kind: "freshness.digest",
    label: "Daily digest",
    hint: "One email each morning listing your pages past their review date and the approvals waiting for you.",
  },
];

// EmailSettings is the caller's own email choices in this workspace; nobody else's can be changed here.
export function EmailSettings({ workspaceID }: { workspaceID: string }) {
  const qc = useQueryClient();
  const key = ["notification-preferences", workspaceID];
  const prefs = useQuery({ queryKey: key, queryFn: () => notificationApi.get(workspaceID) });
  const save = useMutation({
    mutationFn: (body: Partial<NotificationPrefs>) => notificationApi.set(workspaceID, body),
    onSuccess: (next) => qc.setQueryData(key, next),
  });

  return (
    <section className="rounded border border-border bg-surface p-3 text-xs">
      <header className="mb-2 flex items-center gap-1 text-sm font-semibold">
        <Mail size={12} /> Email
      </header>
      {prefs.isLoading ? (
        <p className="text-muted">Loading…</p>
      ) : prefs.isError ? (
        <p className="text-callout-error">Could not load your email settings.</p>
      ) : (
        <ul className="space-y-2">
          {KINDS.map(({ kind, label, hint }) => {
            const on = prefs.data?.[kind] ?? false;
            return (
              <li key={kind} className="flex items-start gap-3">
                <div className="flex-1">
                  <div className="text-sm">{label}</div>
                  <div className="text-muted">{hint}</div>
                </div>
                <button
                  type="button"
                  role="switch"
                  aria-checked={on}
                  aria-label={label}
                  disabled={save.isPending}
                  onClick={() => save.mutate({ [kind]: !on })}
                  className={`relative h-5 w-9 shrink-0 rounded-full border transition-colors disabled:opacity-40 ${
                    on ? "border-accent bg-accent" : "border-border bg-bg"
                  }`}
                >
                  <span
                    className={`absolute top-0.5 h-3.5 w-3.5 rounded-full transition-all ${
                      on ? "left-[18px] bg-on-accent" : "left-0.5 bg-muted"
                    }`}
                  />
                </button>
              </li>
            );
          })}
        </ul>
      )}
      {save.isError ? <p className="mt-2 text-callout-error">Could not save. Try again.</p> : null}
    </section>
  );
}
