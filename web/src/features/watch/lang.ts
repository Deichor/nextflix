// Language codes of audio and subtitle tracks (no imports: audioPick.ts stays a pure module)

// ISO 639-2 (B and T codes, as ffprobe reports them) -> ISO 639-1
const ISO639_2: Record<string, string> = {
  eng: 'en', tur: 'tr', spa: 'es', fre: 'fr', fra: 'fr', ger: 'de', deu: 'de', ita: 'it', por: 'pt', rus: 'ru',
  jpn: 'ja', kor: 'ko', chi: 'zh', zho: 'zh', cmn: 'zh', yue: 'zh', ara: 'ar', hin: 'hi', dut: 'nl', nld: 'nl',
  swe: 'sv', nor: 'no', nob: 'nb', nno: 'nn', dan: 'da', fin: 'fi', pol: 'pl', cze: 'cs', ces: 'cs', slo: 'sk',
  slk: 'sk', hun: 'hu', rum: 'ro', ron: 'ro', gre: 'el', ell: 'el', heb: 'he', tha: 'th', vie: 'vi', ind: 'id',
  may: 'ms', msa: 'ms', ukr: 'uk', bul: 'bg', hrv: 'hr', srp: 'sr', slv: 'sl', est: 'et', lav: 'lv', lit: 'lt',
  per: 'fa', fas: 'fa', ice: 'is', isl: 'is', tam: 'ta', tel: 'te', ben: 'bn', urd: 'ur', cat: 'ca', baq: 'eu',
  eus: 'eu', glg: 'gl', fil: 'fil', tgl: 'tl', aze: 'az', kaz: 'kk', geo: 'ka', kat: 'ka', arm: 'hy', hye: 'hy',
  alb: 'sq', sqi: 'sq', mac: 'mk', mkd: 'mk', bos: 'bs', lat: 'la', wel: 'cy', cym: 'cy', gle: 'ga', kur: 'ku',
  mal: 'ml', kan: 'kn', mar: 'mr', pan: 'pa', guj: 'gu', afr: 'af', swa: 'sw', amh: 'am', mon: 'mn', khm: 'km',
};

/** ISO 639-1 base code ("en", "tr") from 639-1/639-2/BCP 47 input; '' for unknown / undetermined. */
export function normLang(code?: string | null): string {
  const c = (code || '').trim().toLowerCase().split(/[-_]/)[0];
  if (!c || c === 'und' || c === 'zxx' || c === 'mul' || c === 'mis') return '';
  if (c.length === 3) return ISO639_2[c] || c;
  return c;
}
