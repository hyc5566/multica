"use client";

import { useRef, useState, type KeyboardEvent, type PointerEvent } from "react";
import { useChatStore } from "@multica/core/chat";

type Position = { x: number; y: number };
const clamp = (value: number) => Math.max(0, Math.min(1, value));

export function useChatFabPosition() {
  const position = useChatStore((s) => s.fabPosition);
  const save = useChatStore((s) => s.setFabPosition);
  const [preview, setPreview] = useState<Position | null>(null);
  const suppressClick = useRef(false);
  const drag = useRef<{
    id: number;
    x: number;
    y: number;
    start: Position;
    width: number;
    height: number;
    moved: boolean;
    position: Position;
  } | null>(null);

  function travel(button: HTMLElement) {
    const parent = button.offsetParent as HTMLElement | null;
    const inset = Number.parseFloat(getComputedStyle(button).marginLeft) || 0;
    return {
      width: Math.max(1, (parent?.clientWidth ?? window.innerWidth) - button.offsetWidth - inset * 2),
      height: Math.max(1, (parent?.clientHeight ?? window.innerHeight) - button.offsetHeight - inset * 2),
    };
  }

  function finish(event: PointerEvent<HTMLElement>, cancelled = false) {
    const current = drag.current;
    if (!current || current.id !== event.pointerId) return;
    if (current.moved && !cancelled) save(current.position);
    suppressClick.current = current.moved || cancelled;
    drag.current = null;
    setPreview(null);
    if (event.currentTarget.hasPointerCapture(event.pointerId)) {
      event.currentTarget.releasePointerCapture(event.pointerId);
    }
  }

  const current = preview ?? position;
  return {
    // CSS keeps the full button inside its container when its size changes.
    // Margin shares the existing inset token.
    style: {
      left: `calc((100% - var(--chat-launcher-size) - 2 * var(--chat-launcher-inset)) * ${current.x})`,
      top: `calc((100% - var(--chat-launcher-size) - 2 * var(--chat-launcher-inset)) * ${current.y})`,
      margin: "var(--chat-launcher-inset)",
    },
    onPointerDown(event: PointerEvent<HTMLElement>) {
      if (!event.isPrimary || event.button !== 0) return;
      suppressClick.current = false;
      drag.current = {
        id: event.pointerId, x: event.clientX, y: event.clientY,
        start: position, position, ...travel(event.currentTarget), moved: false,
      };
      event.currentTarget.setPointerCapture(event.pointerId);
    },
    onPointerMove(event: PointerEvent<HTMLElement>) {
      const current = drag.current;
      if (!current || current.id !== event.pointerId) return;
      const dx = event.clientX - current.x;
      const dy = event.clientY - current.y;
      if (!current.moved && Math.hypot(dx, dy) < 6) return;
      current.moved = true;
      current.position = {
        x: clamp(current.start.x + dx / current.width),
        y: clamp(current.start.y + dy / current.height),
      };
      setPreview(current.position);
    },
    onPointerUp: (event: PointerEvent<HTMLElement>) => finish(event),
    onPointerCancel: (event: PointerEvent<HTMLElement>) => finish(event, true),
    onLostPointerCapture: (event: PointerEvent<HTMLElement>) => finish(event, true),
    onClick(event: { detail: number; preventDefault: () => void }) {
      if (event.detail !== 0 && suppressClick.current) {
        event.preventDefault();
        return;
      }
      useChatStore.getState().toggle();
    },
    onKeyDown(event: KeyboardEvent<HTMLElement>) {
      const delta = { ArrowLeft: [-1, 0], ArrowRight: [1, 0], ArrowUp: [0, -1], ArrowDown: [0, 1] }[event.key];
      if (event.key === "Home") {
        event.preventDefault();
        save({ x: 1, y: 1 });
      } else if (delta) {
        event.preventDefault();
        const { width, height } = travel(event.currentTarget);
        save({ x: clamp(position.x + delta[0]! * 24 / width), y: clamp(position.y + delta[1]! * 24 / height) });
      }
    },
  };
}
