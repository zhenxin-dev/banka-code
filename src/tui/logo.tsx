/**
 * Banka Code Logo — Ciallo 流光问候语。
 *
 * @author 真心
 */

import { TextAttributes } from "@opentui/core";
import type { JSX } from "@opentui/solid";
import { For, Show, createSignal, onCleanup, onMount } from "solid-js";
import { fetchHitokoto, type Hitokoto } from "./hitokoto.ts";
import { getTheme } from "./theme.ts";

const VERSION = "v0.1.0";

const WAVE_PALETTE: readonly string[] = [
  "#ff94b8", "#ff7cab", "#ff6699", "#ff8a66", "#ffb347",
  "#ff8a66", "#ff6699", "#ff7cab",
];

const DIM = "#b98e84";
const GREETING = "Ciallo～(∠・ω< )⌒☆";
const FRAME_MS = 80;
const WAVE_SPAN = 4;

function parseHex(hex: string): [number, number, number] {
  const v = parseInt(hex.slice(1), 16);
  return [(v >> 16) & 0xff, (v >> 8) & 0xff, v & 0xff];
}

function lerpHex(a: string, b: string, t: number): string {
  const [ar, ag, ab] = parseHex(a);
  const [br, bg, bb] = parseHex(b);
  const r = Math.round(ar + (br - ar) * t);
  const g = Math.round(ag + (bg - ag) * t);
  const bl = Math.round(ab + (bb - ab) * t);
  return `#${((r << 16) | (g << 8) | bl).toString(16).padStart(6, "0")}`;
}

function charColorAt(i: number, tick: number): string {
  const n = WAVE_PALETTE.length;
  const cycle = n + WAVE_SPAN * 2;
  const pos = (((tick * 0.4) - i * 0.7) % cycle + cycle) % cycle;

  if (pos < WAVE_SPAN) {
    return lerpHex(DIM, WAVE_PALETTE[0] ?? DIM, pos / WAVE_SPAN);
  }
  if (pos < WAVE_SPAN + n) {
    const rawIdx = pos - WAVE_SPAN;
    const lo = Math.floor(rawIdx);
    const hi = (lo + 1) % n;
    return lerpHex(WAVE_PALETTE[lo] ?? DIM, WAVE_PALETTE[hi] ?? DIM, rawIdx - lo);
  }
  const fadeT = 1 - (pos - WAVE_SPAN - n) / WAVE_SPAN;
  return lerpHex(DIM, WAVE_PALETTE[0] ?? DIM, Math.max(0, Math.min(1, fadeT)));
}

export interface LogoProps {
  readonly variant?: "compact" | "splash";
}

export function Logo(_props: LogoProps): JSX.Element {
  const t = getTheme();
  const [tick, setTick] = createSignal(0);
  const [hitokoto, setHitokoto] = createSignal<Hitokoto | null>(null);

  const interval = setInterval(() => setTick((v) => v + 1), FRAME_MS);
  onCleanup(() => clearInterval(interval));

  onMount(() => {
    void fetchHitokoto().then((h) => setHitokoto(h));
  });

  const chars = (): ReadonlyArray<{ readonly ch: string; readonly color: string }> => {
    const now = tick();
    return [...GREETING].map((ch, i) => ({
      ch,
      color: ch === "☆" && now % 10 < 2 ? "#ffffff" : charColorAt(i, now),
    }));
  };

  return (
    <box flexDirection="column" alignItems="center">
      <box flexDirection="row" justifyContent="center">
        <text fg={t.text} attributes={TextAttributes.BOLD}>Banka Code</text>
        <text fg={t.inactive}> {VERSION} </text>
        <text fg="#e8a0b0">🌸 </text>
        <For each={chars()}>
          {(c) => <text fg={c.color}>{c.ch}</text>}
        </For>
      </box>
      <Show when={hitokoto()}>
        {(h: () => Hitokoto) => {
          return (
            <box flexDirection="column" alignItems="stretch" marginBottom={1}>
              <box flexDirection="row" justifyContent="center">
                <text fg={t.inactive}>{`「${h().text}」`}</text>
              </box>
            </box>
          );
        }}
      </Show>
    </box>
  );
}
