// Copyright The DeviceChain Authors
// SPDX-License-Identifier: Apache-2.0

import { ChevronLeft, ChevronRight } from 'lucide-react';
import { useTranslation } from 'react-i18next';
import { cn } from '@/lib/utils';
import { Button } from '@/components/ui/button';

interface PaginationProps {
  pageNumber: number;
  pageSize: number;
  pagination: {
    pageStart: number | null;
    pageEnd: number | null;
    totalRecords: number | null;
  };
  onPageChange: (n: number) => void;
  className?: string;
}

export function Pagination({
  pageNumber,
  pagination,
  onPageChange,
  className,
}: PaginationProps) {
  const { t } = useTranslation('common');
  const { pageStart, pageEnd, totalRecords } = pagination;

  const hasResults = pageEnd != null && pageStart != null && pageEnd >= pageStart;
  const prevDisabled = pageNumber <= 1;
  const nextDisabled =
    !hasResults ||
    (totalRecords != null && pageEnd != null && pageEnd >= totalRecords);

  return (
    <div className={cn('flex items-center justify-between gap-4 py-1', className)}>
      <span className="text-sm text-muted-foreground">
        {hasResults && totalRecords != null
          ? // `count` drives the i18next plural selection (…_one / …_other) as well
            // as the {{count}} interpolation; {{start}}/{{end}} fill the range.
            t('paginationShowing', { start: pageStart, end: pageEnd, count: totalRecords })
          : t('paginationNoResults')}
      </span>
      <div className="flex items-center gap-2">
        <Button
          variant="outline"
          size="sm"
          disabled={prevDisabled}
          onClick={() => onPageChange(pageNumber - 1)}
        >
          <ChevronLeft />
          {t('paginationPrev')}
        </Button>
        <Button
          variant="outline"
          size="sm"
          disabled={nextDisabled}
          onClick={() => onPageChange(pageNumber + 1)}
        >
          {t('paginationNext')}
          <ChevronRight />
        </Button>
      </div>
    </div>
  );
}
