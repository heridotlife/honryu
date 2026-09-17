import { forwardRef, useId } from 'react';
import type { InputHTMLAttributes } from 'react';
import { CircleAlert } from 'lucide-react';

export interface InputProps extends InputHTMLAttributes<HTMLInputElement> {
  label?: string;
  error?: string;
  helperText?: string;
  fullWidth?: boolean;
}

const Input = forwardRef<HTMLInputElement, InputProps>(
  ({ label, error, helperText, fullWidth = false, className = '', id, ...props }, ref) => {
    const generatedId = useId();
    const inputId = id ?? generatedId;

    const inputClasses = [
      'block w-full text-base rounded-lg transition-colors duration-200',
      'focus:outline-none focus:ring-2 focus:ring-offset-0',
      'disabled:opacity-50 disabled:cursor-not-allowed',
      'min-h-[44px] px-4 py-3',
      'border bg-white dark:bg-slate-900 text-slate-900 dark:text-white',
      'placeholder:text-slate-400 dark:placeholder:text-slate-500',
      error
        ? 'border-red-300 dark:border-red-700 focus:ring-red-500 focus:border-red-500'
        : 'border-slate-300 dark:border-slate-700 focus:ring-sky-500 focus:border-sky-500',
      className,
    ]
      .filter(Boolean)
      .join(' ');

    return (
      <div className={fullWidth ? 'w-full' : ''}>
        {label && (
          <label htmlFor={inputId} className="mb-2 block text-sm font-medium text-slate-700 dark:text-slate-300">
            {label}
            {props.required && <span className="ml-1 text-red-500">*</span>}
          </label>
        )}
        <input ref={ref} id={inputId} className={inputClasses} {...props} />
        {error && (
          // Icon + text: the message never rides on colour alone.
          <p className="mt-2 flex items-start gap-1 text-sm text-red-600 dark:text-red-400" role="alert">
            <CircleAlert aria-hidden className="mt-0.5 h-4 w-4 shrink-0" />
            <span>{error}</span>
          </p>
        )}
        {helperText && !error && <p className="mt-2 text-sm text-slate-500 dark:text-slate-400">{helperText}</p>}
      </div>
    );
  }
);

Input.displayName = 'Input';

export default Input;
