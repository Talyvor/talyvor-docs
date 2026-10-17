import { apiRequest } from "./client";

export type NotificationPrefs = { "approval.requested": boolean; "freshness.digest": boolean };

export const notificationApi = {
  get(workspaceID: string) {
    return apiRequest<NotificationPrefs>(`/v1/workspaces/${workspaceID}/notification-preferences`);
  },
  set(workspaceID: string, body: Partial<NotificationPrefs>) {
    return apiRequest<NotificationPrefs>(`/v1/workspaces/${workspaceID}/notification-preferences`, {
      method: "PUT",
      body,
    });
  },
};
