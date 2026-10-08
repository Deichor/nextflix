"use client";

import type { CSSProperties } from 'react';
import {
  Smile, Star, Heart, Rocket, Ghost, Cat, Dog, Flame, Crown, Gamepad2,
  Zap, Sun, Moon, Leaf, Bird, Popcorn, Skull, Bot, Pizza, Snowflake,
  type LucideIcon,
} from 'lucide-react';
import type { Profile } from '@/shared/lib/profile';
import { useT, type MessageKey } from '@/shared/i18n';

export const AVATAR_PRESETS: { key: string; label: MessageKey; icon: LucideIcon }[] = [
  { key: 'smile', label: 'avatars.smile', icon: Smile },
  { key: 'star', label: 'avatars.star', icon: Star },
  { key: 'heart', label: 'avatars.heart', icon: Heart },
  { key: 'rocket', label: 'avatars.rocket', icon: Rocket },
  { key: 'ghost', label: 'avatars.ghost', icon: Ghost },
  { key: 'cat', label: 'avatars.cat', icon: Cat },
  { key: 'dog', label: 'avatars.dog', icon: Dog },
  { key: 'bird', label: 'avatars.bird', icon: Bird },
  { key: 'flame', label: 'avatars.flame', icon: Flame },
  { key: 'crown', label: 'avatars.crown', icon: Crown },
  { key: 'gamepad', label: 'avatars.gamepad', icon: Gamepad2 },
  { key: 'zap', label: 'avatars.zap', icon: Zap },
  { key: 'sun', label: 'avatars.sun', icon: Sun },
  { key: 'moon', label: 'avatars.moon', icon: Moon },
  { key: 'leaf', label: 'avatars.leaf', icon: Leaf },
  { key: 'snowflake', label: 'avatars.snowflake', icon: Snowflake },
  { key: 'popcorn', label: 'avatars.popcorn', icon: Popcorn },
  { key: 'pizza', label: 'avatars.pizza', icon: Pizza },
  { key: 'bot', label: 'avatars.bot', icon: Bot },
  { key: 'skull', label: 'avatars.skull', icon: Skull },
];

export const PROFILE_COLORS = [
  '#E50914', // Netflix red
  '#0071EB', // blue
  '#F5C518', // yellow
  '#46D369', // green
  '#9B5DE5', // purple
  '#FF7A00', // orange
  '#E1307C', // pink
  '#00B8A9', // teal
  '#6D6D6E', // grey
  '#8C4A2F', // brown
];

const ICONS: Record<string, LucideIcon> = Object.fromEntries(AVATAR_PRESETS.map((p) => [p.key, p.icon]));

interface ProfileAvatarProps {
  profile: Pick<Profile, 'avatar' | 'color'> & { name?: string };
  /** px number or any CSS length, e.g. "clamp(84px, 10vw, 180px)" */
  size?: number | string;
  className?: string;
  style?: CSSProperties;
}

export default function ProfileAvatar({ profile, size = 32, className = '', style }: ProfileAvatarProps) {
  const t = useT();
  const Icon = ICONS[profile.avatar];
  const dim = typeof size === 'number' ? `${size}px` : size;
  const initial = (profile.name || '?').trim().charAt(0).toUpperCase() || '?';

  return (
    <div
      className={`relative flex shrink-0 select-none items-center justify-center overflow-hidden text-white ${className}`}
      style={{
        width: dim,
        height: dim,
        borderRadius: '8%',
        backgroundColor: profile.color || '#E50914',
        backgroundImage: 'linear-gradient(135deg, rgba(255,255,255,0.18) 0%, rgba(255,255,255,0) 45%, rgba(0,0,0,0.28) 100%)',
        containerType: 'size',
        ...style,
      }}
      aria-label={profile.name ? t('profiles.avatarOf', { name: profile.name }) : t('profiles.avatarGeneric')}
      role="img"
    >
      {Icon ? (
        <Icon style={{ width: '58%', height: '58%' }} strokeWidth={1.75} aria-hidden />
      ) : (
        <span className="font-bold leading-none" style={{ fontSize: '55cqw' }}>
          {initial}
        </span>
      )}
    </div>
  );
}
