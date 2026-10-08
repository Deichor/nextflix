import { useEffect } from 'react';
import { focusDefault, pushKeyHandler } from '../nav/spatial';
import { isBack } from '../lib/webos';

interface ConfirmDialogProps {
  title: string;
  confirm: string;
  cancel: string;
  onConfirm: () => void;
  onCancel: () => void;
}

export default function ConfirmDialog({ title, confirm, cancel, onConfirm, onCancel }: ConfirmDialogProps) {
  useEffect(() => {
    const raf = requestAnimationFrame(() => focusDefault());
    const off = pushKeyHandler(e => {
      if (!isBack(e)) return false;
      onCancel();
      return true;
    });
    return () => { cancelAnimationFrame(raf); off(); };
  }, [onCancel]);

  return (
    <div className="dialog-backdrop" data-nav-layer="dialog">
      <div className="dialog">
        <h2 className="dialog-title">{title}</h2>
        <div className="dialog-buttons">
          <button type="button" className="btn btn-primary" data-nav onClick={onConfirm}>{confirm}</button>
          <button type="button" className="btn btn-secondary" data-nav data-nav-default onClick={onCancel}>{cancel}</button>
        </div>
      </div>
    </div>
  );
}
