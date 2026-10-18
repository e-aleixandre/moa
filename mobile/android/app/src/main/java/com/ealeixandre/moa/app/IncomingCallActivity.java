package com.ealeixandre.moa.app;

import android.app.Activity;
import android.content.Context;
import android.content.Intent;
import android.graphics.Color;
import android.graphics.Typeface;
import android.graphics.drawable.GradientDrawable;
import android.os.Build;
import android.os.Bundle;
import android.view.Gravity;
import android.view.View;
import android.view.WindowManager;
import android.widget.Button;
import android.widget.LinearLayout;
import android.widget.TextView;

// The call screen over the lock screen: who is calling, Answer and Decline.
// It lives only while the invitation rings and answers without unlocking.
public class IncomingCallActivity extends Activity {

    private static volatile IncomingCallActivity shown;

    private String invite;

    static Intent intent(Context context, IncomingCalls.Ringing ringing) {
        return new Intent(context, IncomingCallActivity.class)
            .addFlags(Intent.FLAG_ACTIVITY_NEW_TASK | Intent.FLAG_ACTIVITY_NO_USER_ACTION)
            .putExtra(IncomingCallNotifier.EXTRA_INVITE, ringing.invite)
            .putExtra(IncomingCallNotifier.EXTRA_NAME, ringing.name);
    }

    // The invitation stopped ringing: the screen goes with it.
    static void dismiss(String invite) {
        IncomingCallActivity activity = shown;
        if (activity != null && invite.equals(activity.invite)) activity.runOnUiThread(activity::finish);
    }

    @Override
    protected void onCreate(Bundle savedInstanceState) {
        super.onCreate(savedInstanceState);
        invite = getIntent().getStringExtra(IncomingCallNotifier.EXTRA_INVITE);
        String name = getIntent().getStringExtra(IncomingCallNotifier.EXTRA_NAME);
        // A stale screen (the process was restarted, the call is over) shows nothing.
        if (invite == null || !invite.equals(NativeCalls.ringingInvite())) {
            finish();
            return;
        }
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.O_MR1) {
            setShowWhenLocked(true);
            setTurnScreenOn(true);
        } else {
            getWindow().addFlags(WindowManager.LayoutParams.FLAG_SHOW_WHEN_LOCKED | WindowManager.LayoutParams.FLAG_TURN_SCREEN_ON);
        }
        getWindow().addFlags(WindowManager.LayoutParams.FLAG_KEEP_SCREEN_ON);
        setContentView(content(name == null || name.isEmpty() ? "moa" : name));
        shown = this;
    }

    @Override
    protected void onDestroy() {
        if (shown == this) shown = null;
        super.onDestroy();
    }

    private View content(String name) {
        float density = getResources().getDisplayMetrics().density;
        LinearLayout root = new LinearLayout(this);
        root.setOrientation(LinearLayout.VERTICAL);
        root.setGravity(Gravity.CENTER_HORIZONTAL);
        root.setBackgroundColor(getColor(R.color.moa_background));
        root.setPadding((int) (24 * density), (int) (96 * density), (int) (24 * density), (int) (48 * density));

        TextView label = text(getString(R.string.call_incoming), 16, getColor(R.color.moa_text_secondary), Typeface.NORMAL);
        TextView who = text(name, 34, getColor(R.color.moa_text), Typeface.BOLD);
        who.setPadding(0, (int) (12 * density), 0, 0);
        root.addView(label);
        root.addView(who);

        View spacer = new View(this);
        root.addView(spacer, new LinearLayout.LayoutParams(1, 0, 1f));

        LinearLayout buttons = new LinearLayout(this);
        buttons.setOrientation(LinearLayout.HORIZONTAL);
        buttons.setGravity(Gravity.CENTER);
        buttons.addView(button(R.string.call_decline, 0xFFD93025, density, v -> {
            NativeCalls.decline(this, invite);
            finish();
        }));
        buttons.addView(button(R.string.call_answer, 0xFF1E8E3E, density, v -> {
            NativeCalls.answer(this, invite);
            finish();
        }));
        root.addView(buttons, new LinearLayout.LayoutParams(LinearLayout.LayoutParams.MATCH_PARENT, LinearLayout.LayoutParams.WRAP_CONTENT));
        return root;
    }

    private TextView text(String value, int sp, int color, int style) {
        TextView view = new TextView(this);
        view.setText(value);
        view.setTextSize(sp);
        view.setTextColor(color);
        view.setTypeface(Typeface.DEFAULT, style);
        view.setGravity(Gravity.CENTER);
        return view;
    }

    private Button button(int label, int color, float density, View.OnClickListener click) {
        Button button = new Button(this);
        button.setText(label);
        button.setAllCaps(false);
        button.setTextSize(18);
        button.setTextColor(Color.WHITE);
        GradientDrawable background = new GradientDrawable();
        background.setColor(color);
        background.setCornerRadius(40 * density);
        button.setBackground(background);
        button.setOnClickListener(click);
        LinearLayout.LayoutParams params = new LinearLayout.LayoutParams(0, (int) (64 * density), 1f);
        params.setMargins((int) (8 * density), 0, (int) (8 * density), 0);
        button.setLayoutParams(params);
        return button;
    }
}
