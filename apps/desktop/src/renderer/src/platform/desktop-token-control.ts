import { useAuthStore } from "@multica/core/auth";
import type { DesktopTokenControl } from "@multica/views/settings";

function session() {
  const user = useAuthStore.getState().user;
  const token = localStorage.getItem("multica_token");
  if (!user || !token) throw new Error("Please sign in to the App again.");
  return { token, userId: user.id };
}

export const desktopTokenControl: DesktopTokenControl = {
  async getCurrentTokenId() {
    const { token, userId } = session();
    return window.daemonAPI.getDesktopTokenId(token, userId);
  },
  async rotate(id) {
    const { token, userId } = session();
    const result = await window.daemonAPI.rotateDesktopToken(token, userId, id);
    if (!result.ok) throw new Error(result.message || "Desktop token rotation failed.");
  },
};
