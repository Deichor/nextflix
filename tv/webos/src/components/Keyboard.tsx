import { useEffect, useRef, useState } from 'react';
import { ArrowBigUp, Delete } from 'lucide-react';
import { useI18n, type Locale } from '../lib/i18n';
import { pushKeyHandler } from '../nav/spatial';
import { KEY } from '../lib/webos';

type Layout = 'search' | 'url' | 'text';

interface KeyboardProps {
  /** search: lowercase words, url: server addresses, text: names (Shift, Turkish letters) */
  layout: Layout;
  value: string;
  onChange: (v: string) => void;
  /** max characters */
  max?: number;
}

// Rows of equal length where possible, so Up / Down keep the column
const SEARCH_ROWS = ['abcdef', 'ghijkl', 'mnopqr', 'stuvwx', 'yz1234', '567890'];
// Turkish alphabetical order (q, w, x in their Latin places): ç after c, ğ after g, ı before i ...
const SEARCH_ROWS_TR = ['abcçde', 'fgğhıi', 'jklmno', 'öpqrsş', 'tuüvwx', 'yz1234', '567890'];
const URL_ROWS = ['1234567890', '.:/-_abcde', 'fghijklmno', 'pqrstuvwxy', 'z'];
const TEXT_ROWS = ['abcçdefgğ', 'hıijklmno', 'öpqrsştuü', "vwxyz'-.", '1234567890'];

// Turkish casing (i -> İ) only with the Turkish UI; ı is always I
function upper(ch: string, locale: Locale): string {
  if (ch === 'ı') return 'I';
  if (ch === 'i') return locale === 'tr' ? 'İ' : 'I';
  return ch.toUpperCase();
}

// Shift is on at the start of a name and after a space, off after a letter
const autoShift = (v: string) => v === '' || v.charAt(v.length - 1) === ' ';

// On-screen keyboard for the remote. A PC keyboard (or the remote's number keys) types too.
export default function Keyboard({ layout, value, onChange, max = 120 }: KeyboardProps) {
  const { t, locale } = useI18n();
  const valueRef = useRef(value);
  valueRef.current = value;
  const changeRef = useRef(onChange);
  changeRef.current = onChange;
  const [shift, setShift] = useState(() => layout === 'text' && autoShift(value));
  const shiftRef = useRef(shift);
  shiftRef.current = shift;
  const text = layout === 'text';

  const set = (v: string) => {
    changeRef.current(v);
    if (text) setShift(autoShift(v));
  };
  const type = (ch: string) => {
    if (valueRef.current.length >= max) return;
    set(valueRef.current + (text && shiftRef.current ? upper(ch, locale) : ch));
  };
  const del = () => set(valueRef.current.slice(0, -1));

  useEffect(() => pushKeyHandler(e => {
    if (e.ctrlKey || e.metaKey || e.altKey) return false;
    if (e.keyCode === KEY.BACKSPACE) {
      if (!valueRef.current) return false;
      set(valueRef.current.slice(0, -1));
      return true;
    }
    const printable = !!e.key && e.key.length === 1;
    if (printable && (e.key !== ' ' || layout !== 'url')) {
      const ch = layout === 'search' ? e.key.toLocaleLowerCase() : e.key;
      if (valueRef.current.length < max) set(valueRef.current + ch);
      return true;
    }
    return false;
  }), [layout, max]);

  const rows = layout === 'url' ? URL_ROWS : text ? TEXT_ROWS : locale === 'tr' ? SEARCH_ROWS_TR : SEARCH_ROWS;

  return (
    <div className={`kbd kbd-${layout}`} data-nav-group={`kbd-${layout}`}>
      {rows.map(row => (
        <div className="kbd-row" key={row}>
          {row.split('').map((ch, i) => (
            <button
              key={ch}
              type="button"
              className="kbd-key"
              data-nav
              data-nav-id={`kbd-${ch}`}
              data-nav-default={row === rows[0] && i === 0 ? '' : undefined}
              onClick={() => type(ch)}
            >
              {text && shift ? upper(ch, locale) : ch}
            </button>
          ))}
        </div>
      ))}
      <div className="kbd-row">
        {text && (
          <button
            type="button"
            className={`kbd-key kbd-wide ${shift ? 'is-active' : ''}`}
            data-nav
            data-nav-id="kbd-shift"
            onClick={() => setShift(!shiftRef.current)}
            aria-label={t('keyboard.shift')}
          >
            <ArrowBigUp className="kbd-icon" />
          </button>
        )}
        {layout !== 'url' && (
          <button type="button" className="kbd-key kbd-wide" data-nav data-nav-id="kbd-space" onClick={() => type(' ')}>
            {t('keyboard.space')}
          </button>
        )}
        <button type="button" className="kbd-key kbd-wide" data-nav data-nav-id="kbd-del" onClick={del} aria-label={t('keyboard.delete')}>
          <Delete className="kbd-icon" />
        </button>
        <button type="button" className="kbd-key kbd-wide" data-nav data-nav-id="kbd-clear" onClick={() => set('')}>
          {t('keyboard.clear')}
        </button>
      </div>
    </div>
  );
}
