// Loaded lazily by upscale.ts, so the shader sources only download when upscaling is turned on
import {
  Anime4K_AutoDownscalePre_x2,
  Anime4K_AutoDownscalePre_x4,
  Anime4K_Clamp_Highlights,
  Anime4K_Restore_CNN_M,
  Anime4K_Restore_CNN_S,
  Anime4K_Restore_CNN_Soft_M,
  Anime4K_Restore_CNN_Soft_S,
  Anime4K_Restore_CNN_Soft_VL,
  Anime4K_Restore_CNN_VL,
  Anime4K_Upscale_CNN_x2_M,
  Anime4K_Upscale_CNN_x2_S,
  Anime4K_Upscale_CNN_x2_VL,
  Anime4K_Upscale_Denoise_CNN_x2_M,
  Anime4K_Upscale_Denoise_CNN_x2_S,
  Anime4K_Upscale_Denoise_CNN_x2_VL,
  PassThrough,
  type Anime4KShaderConstructor,
} from 'anime4k.js';

export type Mode = 'A' | 'B' | 'C';
/** X: the secondary mode (A+A, B+B, C+A) in HQ sizes; M and S: the primary mode in that CNN size */
export type Tier = 'X' | 'M' | 'S';

const AUTO_DOWNSCALE = [Anime4K_AutoDownscalePre_x2, Anime4K_AutoDownscalePre_x4];

const PRIMARY: Record<Mode, Record<'M' | 'S', Anime4KShaderConstructor[]>> = {
  A: { M: [Anime4K_Restore_CNN_M, Anime4K_Upscale_CNN_x2_M], S: [Anime4K_Restore_CNN_S, Anime4K_Upscale_CNN_x2_S] },
  B: { M: [Anime4K_Restore_CNN_Soft_M, Anime4K_Upscale_CNN_x2_M], S: [Anime4K_Restore_CNN_Soft_S, Anime4K_Upscale_CNN_x2_S] },
  C: { M: [Anime4K_Upscale_Denoise_CNN_x2_M], S: [Anime4K_Upscale_Denoise_CNN_x2_S] },
};

// Anime4K's HQ secondary modes, in its order (AutoDownscalePre between the stages)
const SECONDARY: Record<Mode, Anime4KShaderConstructor[]> = {
  A: [Anime4K_Restore_CNN_VL, Anime4K_Upscale_CNN_x2_VL, Anime4K_Restore_CNN_M, ...AUTO_DOWNSCALE, Anime4K_Upscale_CNN_x2_M],
  B: [Anime4K_Restore_CNN_Soft_VL, Anime4K_Upscale_CNN_x2_VL, ...AUTO_DOWNSCALE, Anime4K_Restore_CNN_Soft_M, Anime4K_Upscale_CNN_x2_M],
  C: [Anime4K_Upscale_Denoise_CNN_x2_VL, ...AUTO_DOWNSCALE, Anime4K_Restore_CNN_M, Anime4K_Upscale_CNN_x2_M],
};

/**
 * Shader chain of a mode, after Clamp_Highlights. Primary modes leave out AutoDownscalePre and the
 * second upscale: the output is at most 2x the video, so they would never run. Upscale passes only
 * run when the output is over 1.2x the picture.
 */
export function chain(mode: Mode, tier: Tier): Anime4KShaderConstructor[] {
  return [Anime4K_Clamp_Highlights, ...(tier === 'X' ? SECONDARY[mode] : PRIMARY[mode][tier])];
}

export { PassThrough };
