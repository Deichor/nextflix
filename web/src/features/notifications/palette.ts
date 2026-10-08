// Genre-based accent colors for notification tiles
export interface DominantColors {
    primary: string;
    secondary: string;
    accent: string;
    background: string;
    text: string;
}

// Default color palettes by genre
const genreColorPalettes: Record<string, DominantColors> = {
    action: {
        primary: '#e50914',
        secondary: '#b8860b',
        accent: '#ff4500',
        background: 'linear-gradient(135deg, #1a0000 0%, #330000 50%, #000000 100%)',
        text: '#ffffff',
    },
    comedy: {
        primary: '#f5c518',
        secondary: '#ff8c00',
        accent: '#ffd700',
        background: 'linear-gradient(135deg, #1a1500 0%, #332a00 50%, #000000 100%)',
        text: '#ffffff',
    },
    drama: {
        primary: '#7b68ee',
        secondary: '#483d8b',
        accent: '#9370db',
        background: 'linear-gradient(135deg, #0d0d1a 0%, #1a1a33 50%, #000000 100%)',
        text: '#ffffff',
    },
    horror: {
        primary: '#8b0000',
        secondary: '#2f0000',
        accent: '#dc143c',
        background: 'linear-gradient(135deg, #0a0000 0%, #1a0000 50%, #000000 100%)',
        text: '#ffffff',
    },
    scifi: {
        primary: '#00bfff',
        secondary: '#1e90ff',
        accent: '#00ced1',
        background: 'linear-gradient(135deg, #000d1a 0%, #001a33 50%, #000000 100%)',
        text: '#ffffff',
    },
    romance: {
        primary: '#ff69b4',
        secondary: '#db7093',
        accent: '#ff1493',
        background: 'linear-gradient(135deg, #1a0d14 0%, #330d1a 50%, #000000 100%)',
        text: '#ffffff',
    },
    thriller: {
        primary: '#4169e1',
        secondary: '#191970',
        accent: '#6495ed',
        background: 'linear-gradient(135deg, #0a0a1a 0%, #14142b 50%, #000000 100%)',
        text: '#ffffff',
    },
    default: {
        primary: '#e50914',
        secondary: '#831010',
        accent: '#ff6b6b',
        background: 'linear-gradient(135deg, #141414 0%, #1a1a1a 50%, #000000 100%)',
        text: '#ffffff',
    },
};

// Get color palette by genre
export function getColorPaletteByGenre(genres?: string[]): DominantColors {
    if (!genres || genres.length === 0) return genreColorPalettes.default;

    const primaryGenre = genres[0].toLowerCase();

    if (primaryGenre.includes('action')) return genreColorPalettes.action;
    if (primaryGenre.includes('comedy')) return genreColorPalettes.comedy;
    if (primaryGenre.includes('drama')) return genreColorPalettes.drama;
    if (primaryGenre.includes('horror')) return genreColorPalettes.horror;
    if (primaryGenre.includes('sci') || primaryGenre.includes('fiction')) return genreColorPalettes.scifi;
    if (primaryGenre.includes('romance')) return genreColorPalettes.romance;
    if (primaryGenre.includes('thriller')) return genreColorPalettes.thriller;

    return genreColorPalettes.default;
}
