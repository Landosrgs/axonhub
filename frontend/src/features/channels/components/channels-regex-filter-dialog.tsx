'use client';

import { useEffect } from 'react';
import { z } from 'zod';
import { useForm, useFieldArray } from 'react-hook-form';
import { zodResolver } from '@hookform/resolvers/zod';
import { useTranslation } from 'react-i18next';
import { toast } from 'sonner';
import { IconPlus, IconTrash } from '@tabler/icons-react';
import { Button } from '@/components/ui/button';
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card';
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from '@/components/ui/dialog';
import { Form, FormField, FormItem, FormMessage, FormControl } from '@/components/ui/form';
import { Input } from '@/components/ui/input';
import { Switch } from '@/components/ui/switch';
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select';
import { useUpdateChannel } from '../data/channels';
import { Channel, RegexFilterRule } from '../data/schema';
import { mergeChannelSettingsForUpdate } from '../utils/merge';

interface Props {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  currentRow: Channel;
}

const regexFilterFormSchema = z.object({
  rules: z.array(
    z.object({
      pattern: z.string().min(1, 'Pattern is required'),
      mode: z.enum(['regex', 'anchor']),
      enabled: z.boolean(),
    })
  ),
});

type FormValues = z.infer<typeof regexFilterFormSchema>;

export function ChannelsRegexFilterDialog({ open, onOpenChange, currentRow }: Props) {
  const { t } = useTranslation();
  const updateChannel = useUpdateChannel();

  const form = useForm<FormValues>({
    resolver: zodResolver(regexFilterFormSchema),
    defaultValues: {
      rules: (currentRow.settings?.regexFilters as RegexFilterRule[]) || [],
    },
  });

  const { fields, append, remove } = useFieldArray({
    control: form.control,
    name: 'rules',
  });

  useEffect(() => {
    if (open) {
      form.reset({
        rules: (currentRow.settings?.regexFilters as RegexFilterRule[]) || [],
      });
    }
  }, [open, currentRow, form]);

  const onSubmit = async (values: FormValues) => {
    try {
      const nextSettings = mergeChannelSettingsForUpdate(currentRow.settings, {
        regexFilters: values.rules,
      });

      await updateChannel.mutateAsync({
        id: currentRow.id,
        input: {
          settings: nextSettings,
        },
      });
      toast.success(t('channels.messages.updateSuccess'));
      onOpenChange(false);
    } catch (_error) {
      toast.error(t('channels.messages.updateError'));
    }
  };

  return (
    <Dialog
      open={open}
      onOpenChange={(state) => {
        if (!state) {
          form.reset();
        }
        onOpenChange(state);
      }}
    >
      <DialogContent className='sm:max-w-2xl'>
        <DialogHeader className='text-left'>
          <DialogTitle>{t('channels.dialogs.regexFilter.title')}</DialogTitle>
          <DialogDescription>{t('channels.dialogs.regexFilter.description', { name: currentRow.name })}</DialogDescription>
        </DialogHeader>

        <div className='space-y-6'>
          <Card>
            <CardHeader>
              <div className='flex items-center justify-between'>
                <div>
                  <CardTitle className='text-lg'>{t('channels.dialogs.regexFilter.rules.title')}</CardTitle>
                  <CardDescription>{t('channels.dialogs.regexFilter.rules.description')}</CardDescription>
                </div>
                <Button
                  type='button'
                  variant='outline'
                  size='sm'
                  onClick={() => append({ pattern: '', mode: 'anchor', enabled: true })}
                >
                  <IconPlus className='mr-1 h-4 w-4' />
                  {t('channels.dialogs.regexFilter.rules.add')}
                </Button>
              </div>
            </CardHeader>
            <CardContent className='space-y-3'>
              <Form {...form}>
                <form className='space-y-3'>
                  {fields.length === 0 && (
                    <p className='text-muted-foreground text-center text-sm py-4'>
                      {t('channels.dialogs.regexFilter.rules.empty')}
                    </p>
                  )}
                  {fields.map((field, index) => (
                    <div key={field.id} className='flex items-start gap-2 rounded-lg border p-3'>
                      <div className='flex-1 space-y-2'>
                        <FormField
                          control={form.control}
                          name={`rules.${index}.pattern`}
                          render={({ field }) => (
                            <FormItem>
                              <FormControl>
                                <Input
                                  {...field}
                                  placeholder={t('channels.dialogs.regexFilter.rules.patternPlaceholder')}
                                  className='font-mono text-sm'
                                />
                              </FormControl>
                              <FormMessage />
                            </FormItem>
                          )}
                        />
                        <FormField
                          control={form.control}
                          name={`rules.${index}.mode`}
                          render={({ field }) => (
                            <FormItem>
                              <Select onValueChange={field.onChange} value={field.value}>
                                <FormControl>
                                  <SelectTrigger className='w-full'>
                                    <SelectValue />
                                  </SelectTrigger>
                                </FormControl>
                                <SelectContent>
                                  <SelectItem value='anchor'>
                                    {t('channels.dialogs.regexFilter.modes.anchor')}
                                  </SelectItem>
                                  <SelectItem value='regex'>
                                    {t('channels.dialogs.regexFilter.modes.regex')}
                                  </SelectItem>
                                </SelectContent>
                              </Select>
                              <p className='text-muted-foreground text-xs'>
                                {field.value === 'anchor'
                                  ? t('channels.dialogs.regexFilter.modes.anchorHint')
                                  : t('channels.dialogs.regexFilter.modes.regexHint')}
                              </p>
                              <FormMessage />
                            </FormItem>
                          )}
                        />
                      </div>
                      <div className='flex items-center gap-2 pt-2'>
                        <FormField
                          control={form.control}
                          name={`rules.${index}.enabled`}
                          render={({ field }) => (
                            <FormItem>
                              <FormControl>
                                <Switch checked={field.value} onCheckedChange={field.onChange} />
                              </FormControl>
                            </FormItem>
                          )}
                        />
                        <Button type='button' variant='ghost' size='icon' onClick={() => remove(index)} className='h-8 w-8 text-destructive'>
                          <IconTrash className='h-4 w-4' />
                        </Button>
                      </div>
                    </div>
                  ))}
                </form>
              </Form>
            </CardContent>
          </Card>
        </div>

        <DialogFooter>
          <Button type='button' variant='outline' onClick={() => onOpenChange(false)}>
            {t('common.buttons.cancel')}
          </Button>
          <Button type='button' onClick={form.handleSubmit(onSubmit)} disabled={updateChannel.isPending}>
            {updateChannel.isPending ? t('common.buttons.saving') : t('common.buttons.save')}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
