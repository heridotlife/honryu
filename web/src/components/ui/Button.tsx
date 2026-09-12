import { forwardRef } from 'react';
import type { ButtonHTMLAttributes, ReactNode } from 'react';

export interface ButtonProps extends ButtonHTMLAttributes<HTMLButtonElement> {
  variant?: 'primary' | 'accent' | 'secondary' | 'outline' | 'ghost' | 'destructive';
  size?: 'sm' | 'md' | 'lg';
  fullWidth?: boolean;
  children?: ReactNode;
}

const sizeClasses: Record<NonNullable<ButtonProps['size']>, string> = {
  sm: 'px-3 py-2 text-sm min-w-[44px]',
  md: 'px-4 py-2.5 text-base min-w-[44px]',
  lg: 'px-6 py-3 text-lg min-w-[44px]',
};

const variantClasses: Record<NonNullable<ButtonProps['variant']>, string> = {
  primary: [
    'bg-gradient-to-r from-sky-500 to-cyan-500 dark:from-sky-600 dark:to-cyan-600',
    'text-white shadow-md hover:shadow-lg',
    'hover:from-sky-600 hover:to-cyan-600 dark:hover:from-sky-700 dark:hover:to-cyan-700',
    'focus:ring-sky-500',
  ].join(' '),
  // The accent (phase 52): the ONE primary CTA per view reads amber so it
  // stands apart from the sky family that carries navigation and links.
  // Flat amber, not a gradient -- the gradient is primary's signature; a
  // second gradient would blur which control is which.
  accent: [
    'bg-amber-600 dark:bg-amber-600',
    'text-white shadow-md hover:shadow-lg',
    'hover:bg-amber-700 dark:hover:bg-amber-500',
    'focus:ring-amber-500',
  ].join(' '),
  secondary: [
    'border border-slate-300 dark:border-slate-700',
    'bg-white dark:bg-slate-800 text-slate-700 dark:text-slate-200',
    'hover:bg-slate-50 dark:hover:bg-slate-700',
    'focus:ring-sky-500',
  ].join(' '),
  outline: [
    'border border-slate-300 dark:border-slate-700',
    'text-slate-700 dark:text-slate-200',
    'hover:bg-slate-50 dark:hover:bg-slate-800',
    'focus:ring-sky-500',
  ].join(' '),
  ghost: [
    'text-slate-600 dark:text-slate-300',
    'hover:bg-slate-100 dark:hover:bg-slate-800',
    'focus:ring-sky-500',
  ].join(' '),
  // Destructive actions (the execution hub's Purge): reads as danger at
  // rest, not only on hover -- the outline shape keeps it in the same
  // family as the other lifecycle controls while the reds do the talking.
  destructive: [
    'border border-red-600 dark:border-red-400/40',
    'text-red-600 dark:text-red-400',
    'hover:bg-red-50 dark:hover:bg-red-950',
    'focus:ring-red-500',
  ].join(' '),
};

const Button = forwardRef<HTMLButtonElement, ButtonProps>(
  ({ variant = 'primary', size = 'md', fullWidth = false, className = '', type = 'button', ...props }, ref) => {
    const classes = [
      'inline-flex items-center justify-center font-medium rounded-lg',
      'transition-all duration-200',
      'focus:outline-none focus:ring-2 focus:ring-offset-2',
      'disabled:opacity-50 disabled:cursor-not-allowed',
      'min-h-[44px]',
      sizeClasses[size],
      variantClasses[variant],
      fullWidth ? 'w-full' : '',
      className,
    ]
      .filter(Boolean)
      .join(' ');

    return <button ref={ref} type={type} className={classes} {...props} />;
  }
);

Button.displayName = 'Button';

export default Button;
