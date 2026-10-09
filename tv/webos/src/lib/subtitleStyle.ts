import { useEffect, useState } from 'react';
import type { MessageKey } from './i18n';
import { getProfileId } from './profile';
import { readPlayerPrefs, writePlayerPrefs } from './stream';

// Subtitle appearance, per profile in hfx-player:<profileId> (subSize like the web app, the rest in subStyle).
// The server keeps no per-profile subtitle look, so this stays on the TV.

export interface SubtitleStyle {
  size: 'small' | 'medium' | 'large' | 'xlarge';
  bg: 'none' | 'semi' | 'black';
  window: 'none' | 'light' | 'dark';
  edge: 'shadow' | 'outline' | 'none';
  color: 'white' | 'yellow' | 'cyan';
  pos: 'bottom' | 'raised';
}

export type SubStyleKey = keyof SubtitleStyle;

// Today's look
export const DEFAULT_SUB_STYLE: SubtitleStyle = { size: 'medium', bg: 'none', window: 'none', edge: 'shadow', color: 'white', pos: 'bottom' };

export const SUB_STYLE_OPTIONS: { key: SubStyleKey; label: MessageKey; values: { value: string; label: MessageKey }[] }[] = [
  { key: 'size', label: 'subStyle.size', values: [
    { value: 'small', label: 'subStyle.sizeSmall' },
    { value: 'medium', label: 'subStyle.sizeMedium' },
    { value: 'large', label: 'subStyle.sizeLarge' },
    { value: 'xlarge', label: 'subStyle.sizeXLarge' },
  ] },
  { key: 'bg', label: 'subStyle.bg', values: [
    { value: 'none', label: 'subStyle.none' },
    { value: 'semi', label: 'subStyle.bgSemi' },
    { value: 'black', label: 'subStyle.bgBlack' },
  ] },
  { key: 'window', label: 'subStyle.window', values: [
    { value: 'none', label: 'subStyle.none' },
    { value: 'light', label: 'subStyle.windowLight' },
    { value: 'dark', label: 'subStyle.windowDark' },
  ] },
  { key: 'edge', label: 'subStyle.edge', values: [
    { value: 'shadow', label: 'subStyle.edgeShadow' },
    { value: 'outline', label: 'subStyle.edgeOutline' },
    { value: 'none', label: 'subStyle.none' },
  ] },
  { key: 'color', label: 'subStyle.color', values: [
    { value: 'white', label: 'subStyle.colorWhite' },
    { value: 'yellow', label: 'subStyle.colorYellow' },
    { value: 'cyan', label: 'subStyle.colorCyan' },
  ] },
  { key: 'pos', label: 'subStyle.pos', values: [
    { value: 'bottom', label: 'subStyle.posBottom' },
    { value: 'raised', label: 'subStyle.posRaised' },
  ] },
];

const optionOf = (key: SubStyleKey) => SUB_STYLE_OPTIONS.find(o => o.key === key)!;
const valid = (key: SubStyleKey, v: unknown) => optionOf(key).values.some(x => x.value === v);

export function readSubtitleStyle(pid: number | null = getProfileId()): SubtitleStyle {
  const prefs = readPlayerPrefs(pid);
  const stored: Record<string, unknown> = { ...(prefs.subStyle || {}), size: prefs.subSize };
  const out = { ...DEFAULT_SUB_STYLE } as Record<SubStyleKey, string>;
  SUB_STYLE_OPTIONS.forEach(o => { if (valid(o.key, stored[o.key])) out[o.key] = stored[o.key] as string; });
  return out as unknown as SubtitleStyle;
}

const listeners = new Set<() => void>();

export function writeSubtitleStyle(next: SubtitleStyle, pid: number | null = getProfileId()): void {
  const { size, ...rest } = next;
  writePlayerPrefs(pid, { subSize: size, subStyle: rest });
  listeners.forEach(fn => fn());
}

/** The next (or previous) value of one setting, wrapping around. */
export function cycleSubtitleStyle(style: SubtitleStyle, key: SubStyleKey, step: 1 | -1): SubtitleStyle {
  const values = optionOf(key).values;
  const i = values.findIndex(v => v.value === style[key]);
  const next = values[(i + step + values.length) % values.length].value;
  return { ...style, [key]: next } as SubtitleStyle;
}

export function subStyleValueLabel(style: SubtitleStyle, key: SubStyleKey): MessageKey {
  const v = optionOf(key).values.find(x => x.value === style[key]);
  return (v || optionOf(key).values[0]).label;
}

/** Class names for the subtitle overlay (styles.css: .sub-*) */
export const subStyleClass = (s: SubtitleStyle) =>
  `sub-size-${s.size} sub-bg-${s.bg} sub-edge-${s.edge} sub-color-${s.color} sub-pos-${s.pos}`;

/** The active profile's style; updates when the player panel or Settings change it. */
export function useSubtitleStyle(): SubtitleStyle {
  const [style, setStyle] = useState(readSubtitleStyle);
  useEffect(() => {
    const fn = () => setStyle(readSubtitleStyle());
    listeners.add(fn);
    return () => { listeners.delete(fn); };
  }, []);
  return style;
}
