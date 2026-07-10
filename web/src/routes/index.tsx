import { Button } from '@/components/ui/button';

export default function IndexPage() {
  return (
    <div>
      <h1>af-hub</h1>
      <p>Version: {__APP_VERSION__}</p>
      <Button>Get Started</Button>
    </div>
  );
}
