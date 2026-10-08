import { normLang } from '@/features/watch/lang';

// Default audio track. Pure (no browser or app imports); the TV app has a copy in
// tv/webos/src/lib/subtitles.ts.

export interface AudioTrackInfo {
  index: number;
  language: string;
  title: string;
  codec: string;
  channels: number;
  /** default disposition in the file (probe answer) */
  default?: boolean;
}

/** PlayerPrefs.audioLang value for "always the original language" (AUDIO_ORIGINAL in settings/playerPrefs) */
const ORIGINAL_PREF = 'original';

// Words a track title uses for a language ("English 5.1", "Rus MVO")
const LANG_WORDS: Record<string, string[]> = {
  en: ['english', 'eng'], tr: ['turkish', 'türkçe', 'turkce', 'tur'], ru: ['russian', 'rus', 'русский'],
  es: ['spanish', 'español', 'spa'], fr: ['french', 'français', 'fre'], de: ['german', 'deutsch', 'ger'],
  it: ['italian', 'italiano', 'ita'], ja: ['japanese', 'jpn'], ko: ['korean', 'kor'], zh: ['chinese', 'mandarin'],
  hi: ['hindi'], pt: ['portuguese', 'português', 'por'], uk: ['ukrainian', 'ukr'],
};
const ORIGINAL_WORD = /(^|[^a-z])(orig|orij)|оригинал/i;
const DUB_WORD = /(^|[^a-z])(dub|dubbed|mvo|dvo|avo|voice.?over|dublaj)([^a-z]|$)|дубл|многоголос|двухголос|закадр/i;
const COMMENTARY = /comment|yorum|коммент/i;

const words = (title: string) => title.toLowerCase().split(/[\s.,:;()[\]/|+_-]+/).filter(Boolean);

/** A track whose title says it is the original, or names the original language */
function byTitle(tracks: AudioTrackInfo[], orig: string): AudioTrackInfo | undefined {
  const names = LANG_WORDS[orig] || [];
  return tracks.find(t => {
    const title = t.title || '';
    if (!title || COMMENTARY.test(title)) return false;
    return ORIGINAL_WORD.test(title) || words(title).some(w => names.includes(w));
  });
}

/** An untagged ("und") track, usually the original: one without dub words first, the default one first */
function untagged(tracks: AudioTrackInfo[]): AudioTrackInfo | undefined {
  const plain = tracks.filter(t => !normLang(t.language) && !COMMENTARY.test(t.title || ''));
  const clean = plain.filter(t => !DUB_WORD.test(t.title || ''));
  const pool = clean.length ? clean : plain;
  return pool.find(t => t.default) || pool[0];
}

/**
 * Default track, first match wins:
 * 1. the language the viewer picked last time (not with the 'original' preference)
 * 2. Turkish for a Turkish profile (not with the 'original' preference)
 * 3. a track tagged with the title's original language
 * 4. a track whose title says "Original" / "orig" or names the original language ("English")
 * 5. an untagged track (usually the original) over ones tagged with another language
 * 6. the file's default track
 * 7. the first track
 */
export function pickAudioTrack(
  tracks: AudioTrackInfo[],
  opts: { profileLang: string; originalLang?: string; prefLang?: string },
): number {
  if (tracks.length === 0) return 0;
  const find = (lang?: string) => {
    const l = normLang(lang);
    return l ? tracks.find(t => normLang(t.language) === l) : undefined;
  };
  const keepOriginal = opts.prefLang === ORIGINAL_PREF;
  const orig = normLang(opts.originalLang);
  const pick = (keepOriginal ? undefined : find(opts.prefLang)) ||
    (!keepOriginal && opts.profileLang === 'tr' ? find('tr') : undefined) ||
    find(orig) ||
    byTitle(tracks, orig) ||
    (orig ? untagged(tracks) : undefined) ||
    tracks.find(t => t.default) ||
    tracks[0];
  return pick.index;
}
