import {
  Bird, Bot, Cat, Crown, Dog, Flame, Gamepad2, Ghost, Heart, Leaf, Moon, Pizza, Popcorn, Rocket, Skull,
  Smile, Snowflake, Star, Sun, Zap, type LucideIcon,
} from 'lucide-react';
import type { Profile } from '../lib/profile';

// Same avatar keys as web/src/features/profiles/components/ProfileAvatar.tsx
const ICONS: Record<string, LucideIcon> = {
  smile: Smile, star: Star, heart: Heart, rocket: Rocket, ghost: Ghost, cat: Cat, dog: Dog, bird: Bird,
  flame: Flame, crown: Crown, gamepad: Gamepad2, zap: Zap, sun: Sun, moon: Moon, leaf: Leaf,
  snowflake: Snowflake, popcorn: Popcorn, pizza: Pizza, bot: Bot, skull: Skull,
};

// Picker order, the same as AVATAR_PRESETS on the web
export const AVATAR_KEYS = Object.keys(ICONS);

// Same as PROFILE_COLORS on the web
export const PROFILE_COLORS = [
  '#E50914', '#0071EB', '#F5C518', '#46D369', '#9B5DE5', '#FF7A00', '#E1307C', '#00B8A9', '#6D6D6E', '#8C4A2F',
];

export default function Avatar({ profile, className = '' }: { profile: Pick<Profile, 'avatar' | 'color' | 'name'>; className?: string }) {
  const Icon = ICONS[profile.avatar];
  return (
    <div className={`avatar ${className}`} style={{ backgroundColor: profile.color || '#E50914' }}>
      {Icon ? <Icon className="avatar-icon" strokeWidth={1.75} /> : <span className="avatar-initial">{(profile.name || '?').charAt(0).toUpperCase()}</span>}
    </div>
  );
}
