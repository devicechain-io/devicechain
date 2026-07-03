// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

// A token input for entity create forms (ADR-042 P3): a text field plus a
// "regenerate" button that mints a token from the entity type's mask, inline
// validation against the backend security grammar, and an optional debounced
// availability check. Masks are advisory — the backend enforces only the grammar
// — so a non-conforming (but grammar-valid) token is allowed; the mask drives
// generation and the pattern hint, not a hard block.

import { useEffect, useState } from 'react';
import { AlertCircle, Check, Loader2, RefreshCw } from 'lucide-react';
import { generateToken, isValidToken, resolveMask } from '@devicechain/client';
import { Input } from '@/components/ui/input';
import { Button } from '@/components/ui/button';
import { getTokenMasks } from '@/lib/api/settings';

type Availability = 'idle' | 'checking' | 'available' | 'taken';

interface TokenFieldProps {
  /** The entity type whose mask drives generation (e.g. "device", "dashboard"). */
  entityType: string;
  value: string;
  onChange: (value: string) => void;
  /** A human string (usually the entity's name) used to fill {slug} placeholders. */
  seed?: string;
  /** Returns true when the token is free. Enables the taken/available indicator. */
  checkAvailability?: (token: string) => Promise<boolean>;
  /** Fallback placeholder shown until the mask resolves. */
  placeholder?: string;
  id?: string;
  disabled?: boolean;
}

export function TokenField({
  entityType,
  value,
  onChange,
  seed,
  checkAvailability,
  placeholder,
  id,
  disabled,
}: TokenFieldProps) {
  const [mask, setMask] = useState<string | null>(null);
  const [availability, setAvailability] = useState<Availability>('idle');

  // Resolve this entity type's mask once (cached across fields).
  useEffect(() => {
    let alive = true;
    void getTokenMasks().then((masks) => {
      if (alive) setMask(resolveMask(masks, entityType));
    });
    return () => {
      alive = false;
    };
  }, [entityType]);

  const invalid = value.length > 0 && !isValidToken(value);

  // Debounced availability check: only for a non-empty, grammar-valid token.
  useEffect(() => {
    if (!checkAvailability || value.length === 0 || invalid) {
      setAvailability('idle');
      return;
    }
    setAvailability('checking');
    let alive = true;
    const timer = setTimeout(async () => {
      try {
        const free = await checkAvailability(value);
        if (alive) setAvailability(free ? 'available' : 'taken');
      } catch {
        if (alive) setAvailability('idle');
      }
    }, 350);
    return () => {
      alive = false;
      clearTimeout(timer);
    };
  }, [value, invalid, checkAvailability]);

  const regenerate = async () => {
    if (!mask) return;
    let candidate = generateToken(mask, { seed });
    // Best-effort re-roll if the minted token is already taken.
    if (checkAvailability) {
      for (let i = 0; i < 5; i++) {
        try {
          if (await checkAvailability(candidate)) break;
        } catch {
          break;
        }
        candidate = generateToken(mask, { seed });
      }
    }
    onChange(candidate);
  };

  return (
    <div className="space-y-1">
      <div className="flex gap-2">
        <div className="relative flex-1">
          <Input
            id={id}
            value={value}
            disabled={disabled}
            placeholder={mask ?? placeholder}
            aria-invalid={invalid || availability === 'taken'}
            className="pr-8"
            onChange={(e) => onChange(e.target.value)}
          />
          <span className="pointer-events-none absolute right-2 top-1/2 -translate-y-1/2">
            {availability === 'checking' && (
              <Loader2 className="size-4 animate-spin text-muted-foreground" />
            )}
            {availability === 'available' && <Check className="size-4 text-success" />}
            {availability === 'taken' && <AlertCircle className="size-4 text-destructive" />}
          </span>
        </div>
        <Button
          type="button"
          variant="outline"
          size="icon"
          disabled={disabled || !mask}
          onClick={regenerate}
          title="Generate a token from the mask"
          aria-label="Generate a token from the mask"
        >
          <RefreshCw className="size-4" />
        </Button>
      </div>
      {invalid ? (
        <p className="text-xs text-destructive">
          Only letters, digits, hyphens and underscores; must start with a letter or digit.
        </p>
      ) : availability === 'taken' ? (
        <p className="text-xs text-destructive">That token is already in use.</p>
      ) : mask ? (
        <p className="text-xs text-muted-foreground">
          Pattern: <span className="font-mono">{mask}</span>
        </p>
      ) : null}
    </div>
  );
}
