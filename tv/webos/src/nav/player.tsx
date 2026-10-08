import { createContext, useContext } from 'react';
import type { PlayRequest } from '../lib/stream';

export interface PlayerApi {
  /** Opens the player over the current screen; `onClosed` runs after it closed (progress saved) */
  open: (req: PlayRequest, onClosed?: () => void) => void;
}

export const PlayerContext = createContext<PlayerApi>({ open: () => {} });
export const usePlayer = () => useContext(PlayerContext);
