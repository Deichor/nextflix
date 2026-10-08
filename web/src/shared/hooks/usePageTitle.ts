import { useEffect } from 'react';

export function usePageTitle(title: string) {
  useEffect(() => {
    const previousTitle = document.title;
    document.title = `${title} | Nextflix`;
    
    // Cleanup function to restore previous title if needed
    return () => {
      // Only restore if the title hasn't been changed by another component
      if (document.title === `${title} | Nextflix`) {
        document.title = previousTitle;
      }
    };
  }, [title]);
}