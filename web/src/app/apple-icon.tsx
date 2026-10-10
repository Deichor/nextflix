import { ImageResponse } from 'next/og'

export const size = {
  width: 180,
  height: 180,
}

export const contentType = 'image/png'

// "N" from Inter Black (the navbar wordmark font), cap height = half the icon.
const N_PATH =
  'M49.08 135L49.08 45L74.81 45L96.67 80.34Q98.24 82.87 100.48 86.92Q102.71 90.97 105.13 95.56Q105.67 96.64 106.22 97.61Q106.04 92.42 105.92 87.52Q105.8 81.6 105.8 77.98L105.8 45L130.92 45L130.92 135L105.25 135L84.78 102.14Q82.84 98.88 80.15 94.23Q77.47 89.58 74.51 83.96Q74.14 83.3 73.78 82.63Q73.84 83.05 73.84 83.42Q74.08 89.52 74.14 94.41Q74.2 99.3 74.2 102.14L74.2 135Z'

export default function AppleIcon() {
  return new ImageResponse(
    (
      <div style={{ width: '100%', height: '100%', display: 'flex', background: '#141414' }}>
        <svg width={180} height={180} viewBox="0 0 180 180" xmlns="http://www.w3.org/2000/svg">
          <path d={N_PATH} fill="#E50914" />
        </svg>
      </div>
    ),
    {
      ...size,
    }
  )
}
