import { forwardRef } from 'react';
import type { HTMLAttributes, HTMLAttributes as HTMLAttrs } from 'react';

export interface CardProps extends HTMLAttributes<HTMLDivElement> {
  padding?: 'none' | 'sm' | 'md' | 'lg';
}

const paddingClasses: Record<NonNullable<CardProps['padding']>, string> = {
  none: '',
  sm: 'p-4',
  md: 'p-4 sm:p-6',
  lg: 'p-6 sm:p-8',
};

const Card = forwardRef<HTMLDivElement, CardProps>(({ padding = 'md', className = '', ...props }, ref) => {
  const classes = [
    'rounded-2xl transition-all duration-300',
    'bg-white dark:bg-slate-800',
    'border border-slate-200 dark:border-slate-700',
    'shadow-sm',
    paddingClasses[padding],
    className,
  ]
    .filter(Boolean)
    .join(' ');

  return <div ref={ref} className={classes} {...props} />;
});

Card.displayName = 'Card';

const CardHeader = forwardRef<HTMLDivElement, HTMLAttrs<HTMLDivElement>>(({ className = '', ...props }, ref) => (
  <div ref={ref} className={`mb-4 ${className}`} {...props} />
));
CardHeader.displayName = 'CardHeader';

const CardTitle = forwardRef<HTMLHeadingElement, HTMLAttributes<HTMLHeadingElement>>(
  ({ className = '', ...props }, ref) => (
    // h2, not h3 (phase 52): every CardTitle is a top-level section of a
    // page whose h1 is the page title -- h3 under h1 skipped a level (the
    // operator audit's finding on /executions/:id), which screen readers
    // announce as a broken outline. The visual size lives in the classes
    // below; a heading's LEVEL is not its SIZE.
    <h2
      ref={ref}
      className={`text-lg sm:text-xl font-semibold text-slate-900 dark:text-white ${className}`}
      {...props}
    />
  )
);
CardTitle.displayName = 'CardTitle';

const CardContent = forwardRef<HTMLDivElement, HTMLAttrs<HTMLDivElement>>(({ className = '', ...props }, ref) => (
  <div ref={ref} className={`text-slate-600 dark:text-slate-400 ${className}`} {...props} />
));
CardContent.displayName = 'CardContent';

export default Card;
export { CardHeader, CardTitle, CardContent };
