import { fireEvent, render, screen, cleanup } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { createChatStore, registerChatStore } from "@multica/core/chat";

const h = { store: null as unknown as ReturnType<typeof createChatStore> };
import { useChatFabPosition } from "./use-chat-fab-position";

function Launcher() { return <button {...useChatFabPosition()}>Chat</button>; }

beforeEach(() => {
  const values = new Map<string, string>();
  h.store = createChatStore({ storage: {
    getItem: (key) => values.get(key) ?? null,
    setItem: (key, value) => { values.set(key, value); },
    removeItem: (key) => { values.delete(key); },
  } });
  registerChatStore(h.store);
  // jsdom has no pointer capture or layout engine.
  class Pointer extends MouseEvent {
    pointerId = 1;
    isPrimary = true;
  }
  vi.stubGlobal("PointerEvent", Pointer);
  HTMLElement.prototype.setPointerCapture = vi.fn();
  HTMLElement.prototype.releasePointerCapture = vi.fn();
  HTMLElement.prototype.hasPointerCapture = () => true;
});
afterEach(() => { cleanup(); vi.unstubAllGlobals(); });

function drag(button: HTMLElement) {
  fireEvent.pointerDown(button, { clientX: 300, clientY: 400, button: 0 });
  fireEvent.pointerMove(button, { clientX: 120, clientY: 200 });
}

describe("chat launcher movement", () => {
  it("opens on a tap with small finger jitter", () => {
    render(<Launcher />);
    const button = screen.getByRole("button");
    fireEvent.pointerDown(button, { clientX: 300, clientY: 400, button: 0 });
    fireEvent.pointerMove(button, { clientX: 302, clientY: 401 });
    fireEvent.pointerUp(button);
    fireEvent.click(button, { detail: 1 });
    expect(h.store.getState().isOpen).toBe(true);
    expect(h.store.getState().fabPosition).toEqual({ x: 1, y: 1 });
  });

  it("previews movement, saves on release, and consumes the generated click", () => {
    render(<Launcher />);
    const button = screen.getByRole("button");
    drag(button);
    expect(h.store.getState().fabPosition).toEqual({ x: 1, y: 1 });
    fireEvent.pointerUp(button);
    fireEvent.click(button, { detail: 1 });
    expect(h.store.getState().isOpen).toBe(false);
    expect(h.store.getState().fabPosition.x).toBeLessThan(1);
    expect(h.store.getState().fabPosition.y).toBeLessThan(1);
    // A new tap still opens normally.
    fireEvent.pointerDown(button, { button: 0 });
    fireEvent.pointerUp(button);
    fireEvent.click(button, { detail: 1 });
    expect(h.store.getState().isOpen).toBe(true);
  });

  it("cancels without persisting or opening", () => {
    render(<Launcher />);
    const button = screen.getByRole("button");
    drag(button);
    fireEvent.pointerCancel(button);
    fireEvent.click(button, { detail: 1 });
    expect(h.store.getState().fabPosition).toEqual({ x: 1, y: 1 });
    expect(h.store.getState().isOpen).toBe(false);
  });

  it("clamps dragging and supports keyboard movement, reset, and activation", () => {
    render(<Launcher />);
    const button = screen.getByRole("button");
    drag(button);
    fireEvent.pointerMove(button, { clientX: -10000, clientY: -10000 });
    fireEvent.pointerUp(button);
    expect(h.store.getState().fabPosition).toEqual({ x: 0, y: 0 });
    fireEvent.keyDown(button, { key: "ArrowRight" });
    expect(h.store.getState().fabPosition.x).toBeGreaterThan(0);
    fireEvent.keyDown(button, { key: "Home" });
    expect(h.store.getState().fabPosition).toEqual({ x: 1, y: 1 });
    fireEvent.click(button, { detail: 0 });
    expect(h.store.getState().isOpen).toBe(true);
  });
});
