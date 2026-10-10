import { ImageResponse } from 'next/og'

export const size = {
  width: 32,
  height: 32,
}

export const contentType = 'image/png'

// "N" from Inter Black (the navbar wordmark font), stems snapped to the 32px grid.
// Inlined as a path because ImageResponse cannot load the variable woff2.
const N_PATH =
  'M4 30L4 2L12.17 2L18.4 12.99Q18.85 13.78 19.49 15.04Q20.12 16.3 20.81 17.73Q20.97 18.07 21.13 18.37Q21.08 16.75 21.04 15.23Q21 13.39 21 12.26L21 2L29 2L29 30L20.85 30L15.01 19.78Q14.46 18.76 13.7 17.32Q12.93 15.87 12.09 14.12Q11.98 13.91 11.87 13.71Q11.88 13.84 11.88 13.95Q11.96 15.85 11.98 17.37Q12 18.89 12 19.78L12 30Z'

export default function Icon() {
  return new ImageResponse(
    (
      <svg width={32} height={32} viewBox="0 0 32 32" xmlns="http://www.w3.org/2000/svg">
        <path d={N_PATH} fill="#E50914" />
      </svg>
    ),
    {
      ...size,
    }
  )
}
