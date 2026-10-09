import { useEffect, useState } from 'react';
import SideNav from '../components/SideNav';
import { getServer } from '../lib/config';
import { useT, type MessageKey } from '../lib/i18n';
import { readIntroPrefs, writeIntroPrefs, type IntroPrefs } from '../lib/intro';
import { getProfileId } from '../lib/profile';
import {
  DEFAULT_SUB_STYLE, SUB_STYLE_OPTIONS, cycleSubtitleStyle, subStyleValueLabel, useSubtitleStyle, writeSubtitleStyle,
} from '../lib/subtitleStyle';
import { getHdrMode, playbackCaps, readPlayerPrefs, setHdrMode, writePlayerPrefs, type HdrMode } from '../lib/stream';
import { deviceInfo, type DeviceInfo } from '../lib/webos';
import { useFocusRestore, useRouter } from '../nav/router';
import { SubtitleSample } from '../player/SubtitleOverlay';

const HDR_MODES: HdrMode[] = ['auto', 'on', 'off'];
const HDR_LABEL: Record<HdrMode, MessageKey> = { auto: 'settings.hdrAuto', on: 'settings.hdrOn', off: 'settings.hdrOff' };

export default function SettingsScreen() {
  const t = useT();
  const router = useRouter();
  const pid = getProfileId();
  const [hdr, setHdr] = useState(getHdrMode());
  const [autoplay, setAutoplay] = useState(readPlayerPrefs(pid).autoplayNext !== false);
  const [intro, setIntro] = useState(readIntroPrefs());
  const [device, setDevice] = useState<DeviceInfo | null>(null);
  const subStyle = useSubtitleStyle();
  // The preview shows while a subtitle appearance row has the focus
  const [subFocus, setSubFocus] = useState(false);
  const caps = playbackCaps();
  useEffect(() => { deviceInfo().then(setDevice); }, []);
  useFocusRestore(true, 'settings-server');

  const cycleHdr = () => {
    const next = HDR_MODES[(HDR_MODES.indexOf(hdr) + 1) % HDR_MODES.length];
    setHdrMode(next);
    setHdr(next);
  };

  const toggleIntro = (key: keyof IntroPrefs) => {
    const next = { ...intro, [key]: !intro[key] };
    writeIntroPrefs(next);
    setIntro(next);
  };

  const video = [caps.hevc ? 'HEVC' : 'H.264', caps.hdr ? 'HDR10' : '', caps.dv ? 'Dolby Vision' : '', caps.atmos ? 'Dolby Atmos' : ''].filter(Boolean);

  return (
    <div className="screen settings-screen">
      <SideNav active="settings" />
      <div className="settings-body" data-scroll-y="edge" data-scroll-pad="4">
        <div data-scroll-content>
          <h1 className="page-title">{t('settings.title')}</h1>
          <div className="settings-list" data-nav-group="settings" onFocus={e => setSubFocus((e.target as HTMLElement).hasAttribute('data-sub-style'))}>
            <button type="button" className="setting" data-nav data-nav-id="settings-server" onClick={() => router.push({ name: 'setup' })}>
              <span className="setting-name">{t('settings.server')}</span>
              <span className="setting-value">{getServer()}</span>
            </button>
            <button type="button" className="setting" data-nav data-nav-id="settings-hdr" onClick={cycleHdr}>
              <span className="setting-name">{t('settings.hdr')}</span>
              <span className="setting-value">{t(HDR_LABEL[hdr])}</span>
            </button>
            <button
              type="button"
              className="setting"
              data-nav
              data-nav-id="settings-autoplay"
              onClick={() => {
                writePlayerPrefs(pid, { autoplayNext: !autoplay });
                setAutoplay(!autoplay);
              }}
            >
              <span className="setting-name">{t('settings.autoplayNext')}</span>
              <span className="setting-value">{t(autoplay ? 'settings.on' : 'settings.off')}</span>
            </button>
            <button type="button" className="setting" data-nav data-nav-id="settings-intro" onClick={() => toggleIntro('animation')}>
              <span className="setting-name">{t('settings.introAnimation')}</span>
              <span className="setting-value">{t(intro.animation ? 'settings.on' : 'settings.off')}</span>
            </button>
            <button type="button" className="setting" data-nav data-nav-id="settings-intro-sound" onClick={() => toggleIntro('sound')}>
              <span className="setting-name">{t('settings.introSound')}</span>
              <span className="setting-value">{t(intro.sound ? 'settings.on' : 'settings.off')}</span>
            </button>
            <h2 className="settings-section">{t('subStyle.title')}</h2>
            {SUB_STYLE_OPTIONS.map(o => (
              <button
                key={o.key}
                type="button"
                className="setting"
                data-nav
                data-nav-id={`settings-sub-${o.key}`}
                data-sub-style
                onClick={() => writeSubtitleStyle(cycleSubtitleStyle(subStyle, o.key, 1))}
              >
                <span className="setting-name">{t(o.label)}</span>
                <span className="setting-value">{t(subStyleValueLabel(subStyle, o.key))}</span>
              </button>
            ))}
            <button type="button" className="setting" data-nav data-nav-id="settings-sub-reset" data-sub-style onClick={() => writeSubtitleStyle(DEFAULT_SUB_STYLE)}>
              <span className="setting-name">{t('subStyle.reset')}</span>
            </button>
          </div>
          <dl className="settings-info">
            <dt>{t('settings.device')}</dt>
            <dd>{[device?.modelName, device?.sdkVersion ? `webOS ${device.sdkVersion}` : '', device?.uhd ? 'UHD' : ''].filter(Boolean).join(' · ') || navigator.userAgent}</dd>
            <dt>{t('settings.caps')}</dt>
            <dd>{video.join(', ')} · {caps.audio.map(a => a.toUpperCase()).join(', ')}</dd>
            <dt>{t('settings.version')}</dt>
            <dd>{__APP_VERSION__}</dd>
          </dl>
        </div>
      </div>
      {subFocus && (
        <div className="sub-preview">
          <SubtitleSample text={t('subStyle.sample')} />
        </div>
      )}
    </div>
  );
}
