import { KEY, direction, isBack, type Direction } from '../lib/webos';

// D-pad focus engine on real DOM focus.
//   [data-nav]          focusable element (button, or a div with tabIndex -1)
//   [data-nav-id]       stable id, used to restore focus after a screen remounts
//   [data-nav-group]    remembers its last focused child; moves try the same group first
//   [data-nav-scope]    its row groups are also remembered by name (a remounted row returns to its card)
//   [data-row-index]    a row section: Up/Down try the neighbouring rows before the whole layer
//   [data-nav-default]  focused when a group or layer is entered without memory
//   [data-nav-enter]    the group is entered only with that direction (the side menu: "left")
//   [data-nav-layer]    the last one in the DOM traps focus (player, dialogs)
//   [data-scroll-x/-y]  scroll container that keeps the focused element visible:
//                       "start" aligns it to the start (rows), "edge" scrolls only when needed;
//                       [data-scroll-pad] is the padding in rem, [data-scroll-anchor] the element to align;
//                       its only child [data-scroll-content] is the part that moves (translate3d)

export type KeyHandler = (e: KeyboardEvent) => boolean;

const handlers: KeyHandler[] = [];
const focusListeners = new Set<(el: HTMLElement) => void>();
const groupMemory = new WeakMap<Element, HTMLElement>();
// Last focused data-nav-id per "scope|group" name: survives the group's element being replaced
const groupNameMemory = new Map<string, string>();
let lastFocusedId: string | null = null;
let lastMoveAt = 0;

/** Handlers run before spatial navigation, newest first; returning true consumes the key. */
export function pushKeyHandler(fn: KeyHandler): () => void {
  handlers.push(fn);
  return () => {
    const i = handlers.lastIndexOf(fn);
    if (i >= 0) handlers.splice(i, 1);
  };
}

export function onFocusChange(fn: (el: HTMLElement) => void): () => void {
  focusListeners.add(fn);
  return () => { focusListeners.delete(fn); };
}

function activeLayer(): Element {
  const layers = document.querySelectorAll('[data-nav-layer]');
  return layers.length > 0 ? layers[layers.length - 1] : document.body;
}

const rendered = (el: Element) => el.getClientRects().length > 0;
const focusable = (el: Element) => !(el as HTMLButtonElement).disabled && rendered(el);

function focusables(root: Element): HTMLElement[] {
  return Array.prototype.slice.call(root.querySelectorAll('[data-nav]')).filter(focusable);
}

/** The first (step 1) or last (step -1) focusable of `list`, checking only up to it */
function edgeFocusable(list: NodeListOf<Element>, step: 1 | -1): Element | null {
  for (let i = step > 0 ? 0 : list.length - 1; i >= 0 && i < list.length; i += step) {
    if (focusable(list[i])) return list[i];
  }
  return null;
}

// The root font scales with the viewport width: measured once, again after a resize
let fontPx = 0;
const rootFontPx = () => fontPx || (fontPx = parseFloat(getComputedStyle(document.documentElement).fontSize) || 16);

// ---- Scrolling ----
// A scroll container's [data-scroll-content] child is moved with a composited translate3d (no
// layout or repaint per frame on the TV). Positions are read from offsets and rect differences,
// so a move made while the previous transition still runs lands in the right place.

const DURATION = 220;
const offsets = new WeakMap<Element, number>();

function contentOf(container: HTMLElement): HTMLElement | null {
  const c = container.firstElementChild as HTMLElement | null;
  return c && c.hasAttribute('data-scroll-content') ? c : null;
}

function applyOffset(content: HTMLElement, axis: 'x' | 'y', value: number, smooth: boolean) {
  const prev = offsets.get(content) || 0;
  if (Math.abs(prev - value) < 1 && content.style.transform) return;
  offsets.set(content, value);
  const s = content.style;
  // Its own compositor layer from the first move on (rows that never move stay in their parent's layer)
  if (value !== 0) s.willChange = 'transform';
  s.transition = smooth ? `transform ${DURATION}ms cubic-bezier(0.25, 0.1, 0.25, 1)` : 'none';
  s.transform = axis === 'x' ? `translate3d(${-value}px, 0, 0)` : `translate3d(0, ${-value}px, 0)`;
  // Commit the jump now, so a following smooth move does not animate from the old place
  if (!smooth) void content.offsetWidth;
}

function scrollAxis(el: HTMLElement, container: HTMLElement, axis: 'x' | 'y', smooth: boolean) {
  const mode = container.getAttribute(axis === 'x' ? 'data-scroll-x' : 'data-scroll-y');
  const pad = parseFloat(container.getAttribute('data-scroll-pad') || '0') * rootFontPx();
  const anchor = (axis === 'y' && (el.closest('[data-scroll-anchor]') as HTMLElement | null)) || el;
  const content = contentOf(container);
  if (!content) return;
  const x = axis === 'x';
  const kr = content.getBoundingClientRect();
  const er = anchor.getBoundingClientRect();
  // Content position inside the container (positioned, see styles.css) and the element inside the content
  const base = x ? content.offsetLeft : content.offsetTop;
  const pos = base + (x ? er.left - kr.left : er.top - kr.top);
  const size = x ? er.width : er.height;
  const view = x ? container.clientWidth : container.clientHeight;
  const off = offsets.get(content) || 0;
  const rel = pos - off;
  let target = off;
  if (mode === 'start') target = pos - pad;
  else if (rel < pad) target = pos - pad;
  else if (rel + size > view - pad) target = off + Math.min(rel + size - view + pad, rel - pad);
  // The first and last focusable elements also bring in the text above / below them (as far as they stay in view);
  // a single one is the first (the heading above it stays)
  if (mode === 'edge' && !x) {
    const navs = content.querySelectorAll('[data-nav]');
    const first = edgeFocusable(navs, 1);
    if (first && first.contains(el)) target = Math.min(target, pos + size - view + pad);
    else {
      const end = edgeFocusable(navs, -1);
      if (end && end.contains(el)) target = Math.max(target, pos - pad);
    }
  }
  // Content extent as native scrolling had it: the content box or its last child's far edge
  const last = content.lastElementChild as HTMLElement | null;
  let extent = x ? content.offsetWidth : content.offsetHeight;
  if (last) {
    const lr = last.getBoundingClientRect();
    extent = Math.max(extent, x ? lr.right - kr.left : lr.bottom - kr.top);
  }
  const max = Math.max(0, base + extent - view);
  applyOffset(content, axis, Math.max(0, Math.min(max, target)), smooth);
}

// Old Chromium ignores preventScroll: undo the native scroll focus() may have done
function undoNativeScroll(el: HTMLElement) {
  let c = el.parentElement ? el.parentElement.closest('[data-scroll-x], [data-scroll-y]') : null;
  while (c) {
    if (c.scrollLeft) c.scrollLeft = 0;
    if (c.scrollTop) c.scrollTop = 0;
    c = c.parentElement ? c.parentElement.closest('[data-scroll-x], [data-scroll-y]') : null;
  }
}

export function ensureVisible(el: HTMLElement, smooth = true): void {
  const sx = el.parentElement?.closest('[data-scroll-x]') as HTMLElement | null;
  if (sx) scrollAxis(el, sx, 'x', smooth);
  const sy = el.parentElement?.closest('[data-scroll-y]') as HTMLElement | null;
  if (sy) scrollAxis(el, sy, 'y', smooth);
}

// ---- Focus ----

// Rows only (other groups, like the side menu, start from their default on a new screen)
function groupName(g: Element): string | null {
  const scope = g.closest('[data-row-index]') && g.closest('[data-nav-scope]');
  return scope ? `${scope.getAttribute('data-nav-scope')}|${g.getAttribute('data-nav-group')}` : null;
}

function remember(el: HTMLElement) {
  const id = el.getAttribute('data-nav-id');
  let g = el.parentElement?.closest('[data-nav-group]');
  while (g) {
    groupMemory.set(g, el);
    const name = id && groupName(g);
    if (name) groupNameMemory.set(name, id as string);
    g = g.parentElement?.closest('[data-nav-group]');
  }
}

/** The group's last focused element, if it is still there. */
function memoryOf(g: Element): HTMLElement | null {
  const m = groupMemory.get(g);
  if (m && g.contains(m) && rendered(m)) return m;
  const name = groupName(g);
  const id = name && groupNameMemory.get(name);
  return id ? findById(id, g) : null;
}

export function focusElement(el: HTMLElement, opts: { scroll?: boolean; smooth?: boolean } = {}): void {
  try {
    el.focus({ preventScroll: true });
  } catch {
    el.focus();
  }
  undoNativeScroll(el);
  remember(el);
  const id = el.getAttribute('data-nav-id');
  if (id) lastFocusedId = id;
  if (opts.scroll !== false) ensureVisible(el, opts.smooth !== false);
  focusListeners.forEach(fn => fn(el));
}

function findById(id: string, root: Element = activeLayer()): HTMLElement | null {
  const all = root.querySelectorAll<HTMLElement>('[data-nav-id]');
  for (let i = 0; i < all.length; i++) if (all[i].getAttribute('data-nav-id') === id && rendered(all[i])) return all[i];
  return null;
}

export function focusById(id: string | null | undefined, smooth = false): boolean {
  if (!id) return false;
  const el = findById(id);
  if (!el) return false;
  focusElement(el, { smooth });
  return true;
}

/** Focuses [data-nav-default] inside `root` (the active layer by default), else its first focusable. */
export function focusDefault(root: Element = activeLayer()): boolean {
  const list = focusables(root);
  const el = list.find(e => e.hasAttribute('data-nav-default')) || list[0];
  if (!el) return false;
  focusElement(el, { smooth: false });
  return true;
}

export function focusedId(): string | null {
  const el = document.activeElement as HTMLElement | null;
  return (el && el.getAttribute && el.getAttribute('data-nav-id')) || null;
}

// ---- Spatial move ----

function score(dir: Direction, from: DOMRect, to: DOMRect): number | null {
  const fcx = (from.left + from.right) / 2;
  const fcy = (from.top + from.bottom) / 2;
  const tcx = (to.left + to.right) / 2;
  const tcy = (to.top + to.bottom) / 2;
  let along: number;
  let ortho: number;
  let center: number;
  switch (dir) {
    case 'right':
      if (tcx <= fcx + 1 || to.right <= from.right) return null;
      along = Math.max(0, to.left - from.right);
      ortho = Math.max(0, to.top - from.bottom, from.top - to.bottom);
      center = Math.abs(tcy - fcy);
      break;
    case 'left':
      if (tcx >= fcx - 1 || to.left >= from.left) return null;
      along = Math.max(0, from.left - to.right);
      ortho = Math.max(0, to.top - from.bottom, from.top - to.bottom);
      center = Math.abs(tcy - fcy);
      break;
    case 'down':
      if (tcy <= fcy + 1 || to.bottom <= from.bottom) return null;
      along = Math.max(0, to.top - from.bottom);
      ortho = Math.max(0, to.left - from.right, from.left - to.right);
      center = Math.abs(tcx - fcx);
      break;
    default:
      if (tcy >= fcy - 1 || to.top >= from.top) return null;
      along = Math.max(0, from.top - to.bottom);
      ortho = Math.max(0, to.left - from.right, from.left - to.right);
      center = Math.abs(tcx - fcx);
  }
  return along + ortho * 3 + center * 0.05;
}

/** Focusable elements under `root` that may take focus from `cur` in `dir` (skipping `skip`). */
function candidates(root: Element, cur: HTMLElement, dir: Direction, skip?: Element | null): HTMLElement[] {
  const out: HTMLElement[] = [];
  const list = root.querySelectorAll<HTMLElement>('[data-nav]');
  for (let i = 0; i < list.length; i++) {
    const el = list[i];
    if (el === cur || (el as HTMLButtonElement).disabled || (skip && skip.contains(el))) continue;
    const only = el.closest('[data-nav-enter]');
    if (only && !only.contains(cur) && only.getAttribute('data-nav-enter') !== dir) continue;
    out.push(el);
  }
  return out;
}

// One rect per element: a hidden one (display: none) has an empty rect
function best(dir: Direction, from: DOMRect, list: HTMLElement[]): HTMLElement | null {
  let pick: HTMLElement | null = null;
  let min = Infinity;
  for (let i = 0; i < list.length; i++) {
    const r = list[i].getBoundingClientRect();
    if (r.width === 0 && r.height === 0) continue;
    const s = score(dir, from, r);
    if (s !== null && s < min) { min = s; pick = list[i]; }
  }
  return pick;
}

function innermostGroup(el: Element): Element | null {
  return el.parentElement ? el.parentElement.closest('[data-nav-group]') : null;
}

// Up/Down from a row: the nearest row above or below with a target, measuring only that row
function neighbourRow(cur: HTMLElement, dir: Direction, from: DOMRect): HTMLElement | null {
  const section = cur.closest('[data-row-index]');
  if (!section) return null;
  const step = (s: Element) => (dir === 'down' ? s.nextElementSibling : s.previousElementSibling);
  for (let s = step(section); s && s.hasAttribute('data-row-index'); s = step(s)) {
    const g = s.querySelector('[data-nav-group]');
    if (!g) continue;
    const pick = memoryOf(g) || best(dir, from, candidates(g, cur, dir));
    if (pick) return pick;
  }
  return null;
}

export function move(dir: Direction): boolean {
  const layer = activeLayer();
  const cur = document.activeElement as HTMLElement | null;
  if (!cur || cur === document.body || !layer.contains(cur) || !cur.hasAttribute('data-nav')) {
    if (lastFocusedId && focusById(lastFocusedId)) return true;
    return focusDefault(layer);
  }
  const from = cur.getBoundingClientRect();
  // The current group (a row) first; the rest of the layer is measured only when it has no target
  const group = innermostGroup(cur);
  const inGroup = !!group && layer.contains(group);
  let next = inGroup ? best(dir, from, candidates(group as Element, cur, dir)) : null;
  // Then the neighbouring rows; the whole layer (hero buttons, side menu) only when they have none
  if (!next && inGroup && (dir === 'up' || dir === 'down')) next = neighbourRow(cur, dir, from);
  if (!next) next = best(dir, from, candidates(layer, cur, dir, inGroup ? group : null));
  if (!next) return false;
  // Entering another group: go back to where it was left (Netflix rows), or its default
  const target = innermostGroup(next);
  if (target && target !== group && !target.contains(cur)) {
    const memo = memoryOf(target);
    if (memo) next = memo;
    else {
      const def = target.querySelector<HTMLElement>('[data-nav-default]');
      if (def && rendered(def)) next = def;
    }
  }
  focusElement(next);
  return true;
}

// ---- Global key and pointer wiring ----

let backHandler: () => void = () => {};
export const setBackHandler = (fn: () => void) => { backHandler = fn; };

function onKeyDown(e: KeyboardEvent) {
  for (let i = handlers.length - 1; i >= 0; i--) {
    if (handlers[i](e)) {
      e.preventDefault();
      return;
    }
  }
  const dir = direction(e);
  if (dir) {
    e.preventDefault();
    // Held keys repeat very fast on some remotes: keep the UI responsive
    const now = Date.now();
    if (e.repeat && now - lastMoveAt < 70) return;
    lastMoveAt = now;
    move(dir);
    return;
  }
  if (e.keyCode === KEY.ENTER) {
    const el = document.activeElement as HTMLElement | null;
    if (el && el.tagName === 'INPUT') return;
    e.preventDefault();
    if (el && el.hasAttribute('data-nav')) el.click();
    return;
  }
  if (isBack(e)) {
    const el = document.activeElement as HTMLElement | null;
    if (e.keyCode === KEY.BACKSPACE && el && el.tagName === 'INPUT') return;
    e.preventDefault();
    backHandler();
  }
}

// Magic Remote / mouse: hovering focuses (without scrolling rows under the pointer)
function onMouseOver(e: MouseEvent) {
  const target = e.target as Element | null;
  const el = target && target.closest ? (target.closest('[data-nav]') as HTMLElement | null) : null;
  if (!el || el === document.activeElement || !activeLayer().contains(el)) return;
  // A focused text field (voice typing) keeps the focus until a click
  const cur = document.activeElement;
  if (cur && cur.tagName === 'INPUT') return;
  focusElement(el, { scroll: false });
}

export function initSpatial(): void {
  window.addEventListener('resize', () => { fontPx = 0; });
  window.addEventListener('keydown', onKeyDown);
  document.addEventListener('mouseover', onMouseOver);
}
