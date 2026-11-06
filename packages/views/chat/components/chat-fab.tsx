"use client";

import { AnimatePresence, motion } from "motion/react";
import { useReducedMotion } from "@multica/core/appearance";
import { MessageCircle } from "@multica/ui/icons";
import { useQuery } from "@tanstack/react-query";
import { UI_EASE_IN, UI_EASE_OUT, UI_MOTION_DURATION } from "@multica/ui/lib/motion";
import { cn } from "@multica/ui/lib/utils";
import { useChatStore } from "@multica/core/chat";
import {
  chatSessionsOptions,
  countUnreadChatSessions,
  hasPendingChatTasksOptions,
} from "@multica/core/chat/queries";
import { useWorkspaceId } from "@multica/core/hooks";
import { useShortcut } from "@multica/core/shortcuts";
import {
  Tooltip,
  TooltipTrigger,
  TooltipContent,
} from "@multica/ui/components/ui/tooltip";
import { useIsMobile } from "@multica/ui/hooks/use-mobile";
import { ShortcutKeycaps } from "../../common/shortcut-keycaps";
import { useT } from "../../i18n";

import { useChatFabPosition } from "./use-chat-fab-position";

/**
 * The floating launcher. From `md` up the bottom bar carries quick chat, so
 * this is the launcher for narrow windows only.
 */
export function ChatFab() {
  const { t } = useT("chat");
  const isMobile = useIsMobile();
  const wsId = useWorkspaceId();
  const isOpen = useChatStore((s) => s.isOpen);
  const placement = useChatFabPosition();
  // The keyboard route to this button is only useful if it's discoverable, so
  // the tooltip carries the current binding (Settings → Shortcuts can rebind or
  // clear it, hence the null case).
  const shortcut = useShortcut("toggleChat");
  const { data: sessions = [] } = useQuery(chatSessionsOptions(wsId));
  // FAB only needs a boolean "is anything running", and only while it shows
  // and the window is closed (when open, ChatWindow owns the detailed pending
  // query). Gating on `enabled` keeps the minimised button off the
  // per-message aggregate hot path entirely (MUL-4159).
  const { data: hasPending } = useQuery({
    ...hasPendingChatTasksOptions(wsId),
    enabled: !isOpen && isMobile,
  });

  const reduceMotion = useReducedMotion();
  const visible = !isOpen && isMobile;

  const unreadSessionCount = countUnreadChatSessions(sessions);
  const isRunning = hasPending?.has_pending ?? false;

  // Tooltip text carries the running/unread state on hover; the FAB itself no
  // longer shows an unread-count badge (it duplicated the chat tab's, MUL-4374).
  const tooltip = isRunning
    ? t(($) => $.fab.running)
    : unreadSessionCount > 0
      ? t(($) => $.fab.unread, { count: unreadSessionCount })
      : t(($) => $.fab.default);

  return (
    <AnimatePresence initial={false}>
      {visible && (
        <motion.div
          key="chat-fab"
          initial={reduceMotion ? false : { opacity: 0 }}
          animate={{
            opacity: 1,
            transition: { duration: reduceMotion ? 0 : UI_MOTION_DURATION.fast, ease: UI_EASE_OUT },
          }}
          exit={
            reduceMotion
              ? { opacity: 0, transition: { duration: 0 } }
              : {
                  opacity: 0,
                  transition: { duration: UI_MOTION_DURATION.micro, ease: UI_EASE_IN },
                }
          }
          className="pointer-events-none absolute inset-0 z-50"
        >
        <Tooltip>
          <TooltipTrigger
            {...placement}
            aria-description={t(($) => $.fab.move_hint)}
            aria-label={tooltip}
            className={cn(
              // Geometry comes from the shared tokens so the clearance pages
              // reserve for this corner is derived from the same numbers.
              "pointer-events-auto absolute flex size-[var(--chat-launcher-size)] touch-none select-none cursor-grab active:cursor-grabbing items-center justify-center rounded-full bg-surface-raised text-muted-foreground shadow-[var(--floating-shadow)] ring-1 ring-surface-border transition-[background-color,color,box-shadow] hover:bg-surface-hover hover:text-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring/70 focus-visible:ring-offset-2 focus-visible:ring-offset-page-canvas active:bg-surface-hover",
              // Impulse the button itself while a chat task is running — no
              // outer ring to keep things calm.
              isRunning && "animate-chat-impulse",
            )}
          >
            <MessageCircle className="size-5" />
          </TooltipTrigger>
          <TooltipContent side="top" sideOffset={10}>
            {tooltip}
            <span className="block">{t(($) => $.fab.move_hint)}</span>
            {shortcut ? <ShortcutKeycaps shortcut={shortcut} className="ml-1.5" /> : null}
          </TooltipContent>
        </Tooltip>
        </motion.div>
      )}
    </AnimatePresence>
  );
}
