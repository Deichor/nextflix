// webOS specifics: remote key codes, a minimal Luna service call and app exit

export const KEY = {
  LEFT: 37,
  UP: 38,
  RIGHT: 39,
  DOWN: 40,
  ENTER: 13,
  BACK: 461,
  ESC: 27,
  BACKSPACE: 8,
  PLAY: 415,
  PAUSE: 19,
  PLAY_PAUSE: 179,
  STOP: 413,
  REWIND: 412,
  FAST_FORWARD: 417,
} as const;

export type Direction = 'left' | 'right' | 'up' | 'down';

export function direction(e: KeyboardEvent): Direction | null {
  switch (e.keyCode) {
    case KEY.LEFT: return 'left';
    case KEY.RIGHT: return 'right';
    case KEY.UP: return 'up';
    case KEY.DOWN: return 'down';
  }
  return null;
}

/** The remote's Back key (461), Escape on a PC keyboard. Backspace counts unless text is being typed. */
export function isBack(e: KeyboardEvent): boolean {
  return e.keyCode === KEY.BACK || e.keyCode === KEY.ESC || e.key === 'GoBack' || e.keyCode === KEY.BACKSPACE;
}

interface PalmBridge {
  onservicecallback: (msg: string) => void;
  call(uri: string, params: string): void;
}

type W = Window & {
  PalmServiceBridge?: new () => PalmBridge;
  webOSSystem?: { deviceInfo?: string; platformBack?: () => void };
  PalmSystem?: { deviceInfo?: string; platformBack?: () => void };
};

export const isWebOS = () => {
  const w = window as W;
  return !!(w.webOSSystem || w.PalmSystem || /web0s|webos/i.test(navigator.userAgent));
};

/** One Luna call through the PalmServiceBridge (no webOSTV.js needed). Null off webOS or on failure. */
export function luna<T = Record<string, unknown>>(uri: string, params: object = {}, timeoutMs = 3000): Promise<T | null> {
  const w = window as W;
  if (!w.PalmServiceBridge) return Promise.resolve(null);
  return new Promise(resolve => {
    let done = false;
    const finish = (v: T | null) => { if (!done) { done = true; resolve(v); } };
    try {
      const bridge = new w.PalmServiceBridge!();
      bridge.onservicecallback = msg => {
        try {
          const data = JSON.parse(msg);
          finish(data && data.returnValue !== false ? (data as T) : null);
        } catch {
          finish(null);
        }
      };
      bridge.call(uri, JSON.stringify(params));
      // keep a reference until the answer arrives
      setTimeout(() => { void bridge; finish(null); }, timeoutMs);
    } catch {
      finish(null);
    }
  });
}

export interface DeviceInfo {
  modelName?: string;
  sdkVersion?: string;
  uhd: boolean;
  /** undefined when the TV did not answer (older firmware, not webOS) */
  hdr10?: boolean;
  dolbyVision?: boolean;
  dolbyAtmos?: boolean;
}

const truthy = (v: unknown) => v === true || v === 'true' || v === 1;

let device: Promise<DeviceInfo> | null = null;

export function deviceInfo(): Promise<DeviceInfo> {
  if (device) return device;
  device = (async () => {
    const info: DeviceInfo = { uhd: false };
    const w = window as W;
    try {
      const raw = w.webOSSystem?.deviceInfo || w.PalmSystem?.deviceInfo;
      if (raw) {
        const d = JSON.parse(raw);
        info.modelName = d.modelName;
        if (Number(d.screenWidth) >= 3840) info.uhd = true;
      }
    } catch {
      /* not on webOS */
    }
    const sys = await luna<{ modelName?: string; sdkVersion?: string; UHD?: string }>(
      'luna://com.webos.service.tv.systemproperty/getSystemInfo',
      { keys: ['modelName', 'sdkVersion', 'UHD'] },
    );
    if (sys) {
      info.modelName = sys.modelName || info.modelName;
      info.sdkVersion = sys.sdkVersion;
      if (sys.UHD === 'true') info.uhd = true;
    }
    // The same config keys webOSTV.js deviceInfo() reads for HDR10 / Dolby Vision / Atmos
    const cfg = await luna<{ configs?: Record<string, unknown> }>('luna://com.webos.service.config/getConfigs', {
      configNames: [
        'tv.model.supportHDR', 'tv.config.supportDolbyHDRContents', 'tv.conti.supportDolbyHDRContents',
        'tv.config.supportDolbyAtmos', 'tv.conti.supportDolbyAtmos',
      ],
    });
    const c = cfg?.configs;
    if (c) {
      if ('tv.model.supportHDR' in c) info.hdr10 = truthy(c['tv.model.supportHDR']);
      info.dolbyVision = truthy(c['tv.config.supportDolbyHDRContents']) || truthy(c['tv.conti.supportDolbyHDRContents']);
      info.dolbyAtmos = truthy(c['tv.config.supportDolbyAtmos']) || truthy(c['tv.conti.supportDolbyAtmos']);
    }
    return info;
  })();
  return device;
}

/** Leaves the app (webOS closes it; in a browser tab nothing happens). */
export function exitApp(): void {
  try {
    window.close();
  } catch {
    /* ignore */
  }
}
